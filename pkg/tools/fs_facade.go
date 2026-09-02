package tools

import (
	"regexp"

	"github.com/sipeed/picoclaw/pkg/media"
	fstools "github.com/sipeed/picoclaw/pkg/tools/fs"
)

type (
	ReadFileTool               = fstools.ReadFileTool
	ReadFileLinesTool          = fstools.ReadFileLinesTool
	WriteFileTool              = fstools.WriteFileTool
	ListDirTool                = fstools.ListDirTool
	EditFileTool               = fstools.EditFileTool
	AppendFileTool             = fstools.AppendFileTool
	LoadImageTool              = fstools.LoadImageTool
	SendFileTool               = fstools.SendFileTool
	FileMutationPolicy         = fstools.FileMutationPolicy
	FileMutationSiblingPrefix  = fstools.FileMutationSiblingPrefix
	PreparedFileMutationPolicy = fstools.PreparedFileMutationPolicy
	FileIdentityCatalog        = fstools.FileIdentityCatalog
	FileIdentityCatalogOptions = fstools.FileIdentityCatalogOptions
)

const MaxReadFileSize = fstools.MaxReadFileSize

func NewFileIdentityCatalog(options FileIdentityCatalogOptions) (*FileIdentityCatalog, error) {
	return fstools.NewFileIdentityCatalog(options)
}

func NewPreparedFileMutationPolicy(
	workspace string,
	policy FileMutationPolicy,
) (*PreparedFileMutationPolicy, error) {
	return fstools.NewPreparedFileMutationPolicy(workspace, policy)
}

func NewReadFileTool(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *ReadFileTool {
	return fstools.NewReadFileTool(workspace, restrict, maxReadFileSize, allowPaths...)
}

func NewReadFileBytesTool(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *ReadFileTool {
	return fstools.NewReadFileBytesTool(workspace, restrict, maxReadFileSize, allowPaths...)
}

func NewReadFileLinesTool(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *ReadFileLinesTool {
	return fstools.NewReadFileLinesTool(workspace, restrict, maxReadFileSize, allowPaths...)
}

func NewReadFileLinesToolWithPolicy(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	policy FileMutationPolicy,
	allowPaths ...[]*regexp.Regexp,
) (*ReadFileLinesTool, error) {
	return fstools.NewReadFileLinesToolWithPolicy(
		workspace,
		restrict,
		maxReadFileSize,
		policy,
		allowPaths...,
	)
}

func NewWriteFileTool(
	workspace string,
	restrict bool,
	allowPaths ...[]*regexp.Regexp,
) *WriteFileTool {
	return fstools.NewWriteFileTool(workspace, restrict, allowPaths...)
}

func NewWriteFileToolWithPolicy(
	workspace string,
	restrict bool,
	policy FileMutationPolicy,
	allowPaths ...[]*regexp.Regexp,
) (*WriteFileTool, error) {
	return fstools.NewWriteFileToolWithPolicy(workspace, restrict, policy, allowPaths...)
}

func NewListDirTool(
	workspace string,
	restrict bool,
	allowPaths ...[]*regexp.Regexp,
) *ListDirTool {
	return fstools.NewListDirTool(workspace, restrict, allowPaths...)
}

func NewListDirToolWithPolicy(
	workspace string,
	restrict bool,
	policy FileMutationPolicy,
	allowPaths ...[]*regexp.Regexp,
) (*ListDirTool, error) {
	return fstools.NewListDirToolWithPolicy(workspace, restrict, policy, allowPaths...)
}

func NewEditFileTool(
	workspace string,
	restrict bool,
	allowPaths ...[]*regexp.Regexp,
) *EditFileTool {
	return fstools.NewEditFileTool(workspace, restrict, allowPaths...)
}

func NewEditFileToolWithPolicy(
	workspace string,
	restrict bool,
	policy FileMutationPolicy,
	allowPaths ...[]*regexp.Regexp,
) (*EditFileTool, error) {
	return fstools.NewEditFileToolWithPolicy(workspace, restrict, policy, allowPaths...)
}

func NewAppendFileTool(
	workspace string,
	restrict bool,
	allowPaths ...[]*regexp.Regexp,
) *AppendFileTool {
	return fstools.NewAppendFileTool(workspace, restrict, allowPaths...)
}

func NewAppendFileToolWithPolicy(
	workspace string,
	restrict bool,
	policy FileMutationPolicy,
	allowPaths ...[]*regexp.Regexp,
) (*AppendFileTool, error) {
	return fstools.NewAppendFileToolWithPolicy(workspace, restrict, policy, allowPaths...)
}

func NewLoadImageTool(
	workspace string,
	restrict bool,
	maxFileSize int,
	store media.MediaStore,
	allowPaths ...[]*regexp.Regexp,
) *LoadImageTool {
	return fstools.NewLoadImageTool(workspace, restrict, maxFileSize, store, allowPaths...)
}

func NewSendFileTool(
	workspace string,
	restrict bool,
	maxFileSize int,
	store media.MediaStore,
	allowPaths ...[]*regexp.Regexp,
) *SendFileTool {
	return fstools.NewSendFileTool(workspace, restrict, maxFileSize, store, allowPaths...)
}
