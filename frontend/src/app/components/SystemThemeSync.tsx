'use client';

import { useEffect } from 'react';
import { useAppStore } from '../store/app-store';
import { apiService } from '../services/api';

// 内置基础主题。其余任何值都视为「自定义主题 id」，从安装目录/用户目录的 themes\ 加载。
const BUILTIN_MODES = new Set(['system', 'light', 'dark']);

// 注入自定义主题 CSS 的 <style> 元素 id。
const CUSTOM_STYLE_ID = 'thrm-custom-theme-style';

function isBuiltinMode(mode: string): boolean {
  return BUILTIN_MODES.has(mode);
}

// 清除已注入的自定义主题（移除 <style> 与 <html data-theme>）。
function clearCustomTheme() {
  const el = document.getElementById(CUSTOM_STYLE_ID);
  if (el) el.remove();
  delete document.documentElement.dataset.theme;
}

// 应用内置基础主题：仅切换 .dark，并清掉任何自定义主题残留。
function applyBuiltinMode(mode: string, prefersDark: boolean) {
  clearCustomTheme();
  const isDark = mode === 'dark' || (mode === 'system' && prefersDark);
  document.documentElement.classList.toggle('dark', isDark);
}

/**
 * 应用自定义主题：
 *   1) 从后端拿到主题列表，确定该主题基于浅色还是深色（base）。
 *   2) 读取该主题的 CSS 文本，注入到 <style id> 中。
 *   3) 给 <html> 打上 data-theme="id"，使主题 CSS 的 html[data-theme="id"] 选择器生效。
 * 任意环节失败（如主题文件被删）时，安全回退到 base 对应的浅色/深色基础主题。
 */
async function applyCustomTheme(id: string): Promise<void> {
  let base: 'light' | 'dark' = 'light';
  try {
    const themes = await apiService.listThemes();
    const meta = themes.find((t) => t.id === id);
    if (meta?.base === 'dark') base = 'dark';
  } catch {
    /* 列表获取失败时按浅色基底处理 */
  }

  let css = '';
  try {
    css = await apiService.getThemeCSS(id);
  } catch {
    /* 读取失败下方走回退 */
  }

  if (!css) {
    // 自定义主题不可用：清理并回退到基础主题（按 base 决定浅/深）。
    clearCustomTheme();
    document.documentElement.classList.toggle('dark', base === 'dark');
    return;
  }

  let styleEl = document.getElementById(CUSTOM_STYLE_ID) as HTMLStyleElement | null;
  if (!styleEl) {
    styleEl = document.createElement('style');
    styleEl.id = CUSTOM_STYLE_ID;
    document.head.appendChild(styleEl);
  }
  styleEl.textContent = css;

  // 先设基底明暗，再打 data-theme，避免基础变量覆盖主题变量。
  document.documentElement.classList.toggle('dark', base === 'dark');
  document.documentElement.dataset.theme = id;
}

/**
 * 在 <html> 上写入 data-os：CSS 用 [data-os="mac"] / [data-os="win"]
 * 精确分流字体渲染策略 — 因为 Windows ClearType 子像素和 macOS 灰阶反锯齿
 * 对同一段 CSS 的诉求是反的。
 */
function detectOs(): 'win' | 'mac' | 'linux' | 'other' {
  if (typeof navigator === 'undefined') return 'other';
  const ua = navigator.userAgent || '';
  const platform = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform || '';
  const probe = `${ua} ${platform}`.toLowerCase();
  if (probe.includes('windows') || probe.includes('win32') || probe.includes('win64')) return 'win';
  if (probe.includes('mac') || probe.includes('darwin')) return 'mac';
  if (probe.includes('linux')) return 'linux';
  return 'other';
}

export default function SystemThemeSync() {
  const themeMode = useAppStore((state) => String((state.config as any)?.themeMode ?? 'system'));

  useEffect(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    // effect 重跑（主题切换）时用于丢弃尚未完成的旧异步应用结果。
    let cancelled = false;

    if (isBuiltinMode(themeMode)) {
      applyBuiltinMode(themeMode, media.matches);
    } else {
      void applyCustomTheme(themeMode).then(() => {
        if (cancelled) clearCustomTheme();
      });
    }

    const handleChange = (event: MediaQueryListEvent) => {
      // 仅「跟随系统」时随系统明暗联动；自定义主题的明暗由其 base 固定。
      if (themeMode !== 'system') return;
      clearCustomTheme();
      document.documentElement.classList.toggle('dark', event.matches);
    };

    media.addEventListener('change', handleChange);
    return () => {
      cancelled = true;
      media.removeEventListener('change', handleChange);
    };
  }, [themeMode]);

  // 仅首挂载时打 data-os；UA / 平台不会运行时变化
  useEffect(() => {
    document.documentElement.dataset.os = detectOs();
  }, []);

  return null;
}
