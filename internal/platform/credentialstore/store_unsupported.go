//go:build !darwin && !linux

package credentialstore

type unsupported struct{}

func NewDurable(string) Store                            { return unsupported{} }
func New(string) Store                                   { return unsupported{} }
func (unsupported) Save(string, []byte) error            { return ErrUnsafe }
func (unsupported) Load(string) ([]byte, error)          { return nil, ErrUnsafe }
func (unsupported) Replace(string, []byte, []byte) error { return ErrUnsafe }
func (unsupported) Delete(string, []byte) error          { return ErrUnsafe }
