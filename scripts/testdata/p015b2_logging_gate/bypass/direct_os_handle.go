//go:build p015_inventory_never

package bypass

import o "os"

func directOSHandleMethods(value string) {
	_, _ = o.Stdout.Write([]byte(value))
	_, _ = o.Stderr.WriteString(value)

	stdoutWrite := o.Stdout.Write
	stderrWriteString := o.Stderr.WriteString
	_, _ = stdoutWrite, stderrWriteString
}

type ordinaryWriter struct{}

func (ordinaryWriter) Write(value []byte) (int, error) {
	return len(value), nil
}

func (ordinaryWriter) WriteString(value string) (int, error) {
	return len(value), nil
}

func ordinaryWriterMethods(writer ordinaryWriter, value string) {
	_, _ = writer.Write([]byte(value))
	_, _ = writer.WriteString(value)

	write := writer.Write
	writeString := writer.WriteString
	_, _ = write, writeString
}
