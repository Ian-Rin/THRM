//go:build !windows

package msifan

import (
	"errors"

	"github.com/TIANLI0/THRM/internal/types"
)

type noopController struct{}

func newPlatformController(_ types.Logger) Controller { return noopController{} }

var errUnsupported = errors.New("msifan: 仅支持 Windows")

func (noopController) Init(string) error              { return errUnsupported }
func (noopController) Available() bool                { return false }
func (noopController) Status() (Status, error)        { return Status{}, errUnsupported }
func (noopController) ApplyCurves(_, _ Curve) error   { return errUnsupported }
func (noopController) SetFullBlast(bool) error        { return errUnsupported }
func (noopController) RestoreDefault() error          { return errUnsupported }
func (noopController) Close()                         {}
