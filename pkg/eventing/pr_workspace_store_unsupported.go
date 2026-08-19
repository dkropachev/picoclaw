//go:build mipsle || netbsd || (freebsd && arm)

package eventing

import "context"

var (
	_ PRWorkspaceStore        = (*Store)(nil)
	_ PRWorkspaceCutoverStore = (*Store)(nil)
	_ PRWorkspaceWorkerStore  = (*Store)(nil)
)

func (*Store) SetPRWorkspaceIngressCutover(context.Context, PRIngressCutoverWatermark) error {
	return ErrUnsupportedPlatform
}

func (*Store) GetPRWorkspaceIngressCutover(context.Context, string, string) (PRIngressCutoverWatermark, error) {
	return PRIngressCutoverWatermark{}, ErrUnsupportedPlatform
}

func (*Store) ClaimPRWorkspaceOperations(context.Context, PRWorkspaceClaimRequest) ([]PRClaimedOperationIntent, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) FinishPRWorkspaceOperation(
	context.Context,
	PRWorkspaceOperationFinish,
) (PRClaimedOperationIntent, error) {
	return PRClaimedOperationIntent{}, ErrUnsupportedPlatform
}

func (*Store) ClaimPRWorkspacePublications(context.Context, PRWorkspaceClaimRequest) ([]PRClaimedPublication, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) FinishPRWorkspacePublication(
	context.Context,
	PRWorkspacePublicationFinish,
) (PRClaimedPublication, error) {
	return PRClaimedPublication{}, ErrUnsupportedPlatform
}

func (*Store) CreatePRWorkspace(context.Context, PRWorkspaceCreate) (PRWorkspaceAggregate, bool, error) {
	return PRWorkspaceAggregate{}, false, ErrUnsupportedPlatform
}

func (*Store) GetPRWorkspace(context.Context, string) (PRWorkspaceAggregate, error) {
	return PRWorkspaceAggregate{}, ErrUnsupportedPlatform
}

func (*Store) ListPRWorkspaces(context.Context, PRWorkspaceFilter) (PRWorkspacePage, error) {
	return PRWorkspacePage{}, ErrUnsupportedPlatform
}

func (*Store) ApplyPRWorkspaceMutation(context.Context, PRWorkspaceMutation) (PRWorkspaceMutationResult, error) {
	return PRWorkspaceMutationResult{}, ErrUnsupportedPlatform
}

func (*Store) ApplyPRWorkspacePatch(context.Context, PRWorkspacePatchMutation) (PRWorkspacePatchResult, error) {
	return PRWorkspacePatchResult{}, ErrUnsupportedPlatform
}
