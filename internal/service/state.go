package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/apperr"
	"github.com/nanohype/portal/internal/conv"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/storage"
	"github.com/nanohype/portal/internal/tfstate"
)

// StateService coordinates the two stores behind terraform state: the
// state_versions rows (Postgres) and the raw tfstate blobs (S3). The handler
// owns HTTP shape; this owns the DB-lookup → S3-fetch → parse pipeline that the
// Resources / Outputs / Diff / Download / Delete endpoints each repeated.
//
// Not-found surfaces as apperr.NotFound (→ 404 with a specific message);
// missing storage / fetch / parse failures are plain errors (→ 500, logged).
type StateService struct {
	queries *repository.Queries
	storage *storage.S3Storage
}

func NewStateService(queries *repository.Queries, store *storage.S3Storage) *StateService {
	return &StateService{queries: queries, storage: store}
}

// ListVersions returns a page of a workspace's state-version history.
func (s *StateService) ListVersions(ctx context.Context, workspaceID, orgID string, page, perPage int) ([]repository.StateVersion, error) {
	return s.queries.ListStateVersionsByWorkspace(ctx, repository.ListStateVersionsParams{
		WorkspaceID: workspaceID,
		OrgID:       orgID,
		Limit:       conv.Int32(perPage),
		Offset:      conv.Int32((page - 1) * perPage),
	})
}

// Latest returns the most recent state version for a workspace.
func (s *StateService) Latest(ctx context.Context, workspaceID, orgID string) (repository.StateVersion, error) {
	sv, err := s.queries.GetLatestStateVersion(ctx, repository.GetLatestStateVersionParams{
		WorkspaceID: workspaceID, OrgID: orgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sv, apperr.NotFound("no state found")
	}
	return sv, err
}

// Version returns a single state version of one workspace. The workspace is
// part of the key: the route is authorized against the workspace in its path,
// so a state-version id from elsewhere in the org has to miss.
func (s *StateService) Version(ctx context.Context, stateID, workspaceID, orgID string) (repository.StateVersion, error) {
	sv, err := s.queries.GetStateVersion(ctx, repository.GetStateVersionParams{
		ID: stateID, WorkspaceID: workspaceID, OrgID: orgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sv, apperr.NotFound("state version not found")
	}
	return sv, err
}

// Download returns a state version's metadata and its raw tfstate blob.
func (s *StateService) Download(ctx context.Context, stateID, workspaceID, orgID string) (repository.StateVersion, []byte, error) {
	sv, err := s.Version(ctx, stateID, workspaceID, orgID)
	if err != nil {
		return sv, nil, err
	}
	data, err := s.fetch(ctx, sv.StateURL)
	return sv, data, err
}

// Resources returns the parsed resource list from a workspace's latest state.
//
// The view is the caller's disclosure bar, not a preference: state attributes
// are the tfstate blob's own contents, so only a caller who could download the
// blob gets their values. See tfstate.AttributeView.
func (s *StateService) Resources(ctx context.Context, workspaceID, orgID string, view tfstate.AttributeView) ([]tfstate.Resource, error) {
	data, err := s.latestStateData(ctx, workspaceID, orgID)
	if err != nil {
		return nil, err
	}
	return tfstate.ParseResources(data, view)
}

// Outputs returns the parsed outputs from a workspace's latest state.
func (s *StateService) Outputs(ctx context.Context, workspaceID, orgID string) ([]tfstate.Output, error) {
	data, err := s.latestStateData(ctx, workspaceID, orgID)
	if err != nil {
		return nil, err
	}
	return tfstate.ParseOutputs(data)
}

// Diff compares two state versions of a workspace by serial. The view carries
// the same meaning it does on Resources — the diff's before/after values are
// state attributes.
func (s *StateService) Diff(ctx context.Context, workspaceID, orgID string, fromSerial, toSerial int, view tfstate.AttributeView) (*tfstate.StateDiff, error) {
	fromSV, err := s.bySerial(ctx, workspaceID, orgID, fromSerial, "from")
	if err != nil {
		return nil, err
	}
	toSV, err := s.bySerial(ctx, workspaceID, orgID, toSerial, "to")
	if err != nil {
		return nil, err
	}
	fromData, err := s.fetch(ctx, fromSV.StateURL)
	if err != nil {
		return nil, err
	}
	toData, err := s.fetch(ctx, toSV.StateURL)
	if err != nil {
		return nil, err
	}
	return tfstate.DiffStates(fromData, toData, view)
}

// DeleteVersion drops a state_versions row and best-effort removes its S3
// objects. A non-nil err means the row was not deleted; storageErr is the
// non-fatal S3-cleanup result (the row is already gone, so orphan objects are
// recoverable noise the caller can audit).
func (s *StateService) DeleteVersion(ctx context.Context, workspaceID, orgID string, serial int) (sv repository.StateVersion, storageErr, err error) {
	sv, err = s.queries.DeleteStateVersion(ctx, repository.GetStateVersionBySerialParams{
		WorkspaceID: workspaceID, OrgID: orgID, Serial: conv.Int32(serial),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sv, nil, apperr.NotFound("state version not found")
		}
		return sv, nil, err
	}
	storageErr = s.storage.DeleteStateObjects(ctx, workspaceID, serial)
	return sv, storageErr, nil
}

func (s *StateService) bySerial(ctx context.Context, workspaceID, orgID string, serial int, which string) (repository.StateVersion, error) {
	sv, err := s.queries.GetStateVersionBySerial(ctx, repository.GetStateVersionBySerialParams{
		WorkspaceID: workspaceID, OrgID: orgID, Serial: conv.Int32(serial),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sv, apperr.NotFound("state version not found for '" + which + "' serial")
	}
	return sv, err
}

func (s *StateService) latestStateData(ctx context.Context, workspaceID, orgID string) ([]byte, error) {
	sv, err := s.Latest(ctx, workspaceID, orgID)
	if err != nil {
		return nil, err
	}
	return s.fetch(ctx, sv.StateURL)
}

// fetch pulls a state blob from S3, surfacing a clear error when storage isn't
// configured (a deploy without S3 wired).
func (s *StateService) fetch(ctx context.Context, key string) ([]byte, error) {
	if s.storage == nil {
		return nil, errors.New("storage not configured")
	}
	return s.storage.GetState(ctx, key)
}
