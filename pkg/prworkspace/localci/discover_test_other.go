//go:build !unix

package localci

import "errors"

func syscallMkfifo(string) error {
	return errors.New("FIFO unsupported")
}
