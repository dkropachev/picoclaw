package agent

import (
	"errors"
	"io"
	"io/fs"
)

// AgentDefinitionMaxBytes bounds prompt-definition reads in every runtime path.
const AgentDefinitionMaxBytes = 1 << 20

var (
	// ErrAgentDefinitionNotRegular rejects symlinks and non-regular files.
	ErrAgentDefinitionNotRegular = errors.New("agent definition is not a regular file")
	// ErrAgentDefinitionTooLarge rejects definitions above AgentDefinitionMaxBytes.
	ErrAgentDefinitionTooLarge = errors.New("agent definition exceeds size limit")
)

// AgentDefinitionFile is a bounded, regular prompt file and its permission mode.
type AgentDefinitionFile struct {
	Data []byte
	Mode fs.FileMode
}

// ReadAgentDefinitionFile reads a regular prompt file without following symlinks.
func ReadAgentDefinitionFile(path string) (AgentDefinitionFile, bool, error) {
	file, err := openAgentDefinitionFileNoFollow(path)
	if errors.Is(err, fs.ErrNotExist) {
		return AgentDefinitionFile{}, false, nil
	}
	if err != nil {
		return AgentDefinitionFile{}, false, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return AgentDefinitionFile{}, false, err
	}
	if !info.Mode().IsRegular() {
		return AgentDefinitionFile{}, false, ErrAgentDefinitionNotRegular
	}
	if info.Size() > AgentDefinitionMaxBytes {
		return AgentDefinitionFile{}, false, ErrAgentDefinitionTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, AgentDefinitionMaxBytes+1))
	if err != nil {
		return AgentDefinitionFile{}, false, err
	}
	if len(data) > AgentDefinitionMaxBytes {
		return AgentDefinitionFile{}, false, ErrAgentDefinitionTooLarge
	}
	return AgentDefinitionFile{Data: data, Mode: info.Mode().Perm()}, true, nil
}
