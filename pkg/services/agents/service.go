package agents

import (
	"context"
	"fmt"
	"log/slog"

	v1 "mytonprovider-backend/pkg/models/api/v1"
	"mytonprovider-backend/pkg/models/db"
)

const defaultTaskLimit = 100

type repository interface {
	GetStorageContracts(ctx context.Context) (contracts []db.ContractToProviderRelation, err error)
	UpdateProvidersIPs(ctx context.Context, ips []db.ProviderIP) (err error)
	UpdateContractProofsChecks(ctx context.Context, contractsProofs []db.ContractProofsCheck) (err error)
	UpdateStatuses(ctx context.Context) (err error)
}

type Service interface {
	PollTasks(ctx context.Context, req v1.AgentPollRequest) (v1.AgentPollResponse, error)
	SubmitResult(ctx context.Context, req v1.AgentTaskResultRequest) error
}

type service struct {
	repository repository
	logger     *slog.Logger
}

func NewService(repository repository, logger *slog.Logger) Service {
	return &service{
		repository: repository,
		logger:     logger,
	}
}

func (s *service) PollTasks(ctx context.Context, req v1.AgentPollRequest) (resp v1.AgentPollResponse, err error) {
	limit := req.Limit
	if limit <= 0 || limit > defaultTaskLimit {
		limit = defaultTaskLimit
	}

	contracts, err := s.repository.GetStorageContracts(ctx)
	if err != nil {
		return resp, err
	}
	if len(contracts) == 0 {
		return resp, nil
	}
	if len(contracts) > limit {
		contracts = contracts[:limit]
	}

	resp.Tasks = []v1.AgentTask{{
		ID:        fmt.Sprintf("storage-proof-%d", len(contracts)),
		Kind:      v1.AgentTaskKindStorageProofCheck,
		Contracts: contracts,
	}}

	s.logger.Debug("issued agent tasks", "agent_id", req.AgentID, "tasks", len(resp.Tasks), "contracts", len(contracts))
	return resp, nil
}

func (s *service) SubmitResult(ctx context.Context, req v1.AgentTaskResultRequest) error {
	if req.Error != "" {
		s.logger.Error("agent task failed", "agent_id", req.AgentID, "task_id", req.TaskID, "kind", req.Kind, "error", req.Error)
		return nil
	}

	if req.Kind != v1.AgentTaskKindStorageProofCheck {
		return fmt.Errorf("unsupported agent task result kind: %s", req.Kind)
	}

	if err := s.repository.UpdateProvidersIPs(ctx, req.ProviderIPs); err != nil {
		return err
	}
	if len(req.ContractProofsChecks) > 0 {
		if err := s.repository.UpdateContractProofsChecks(ctx, req.ContractProofsChecks); err != nil {
			return err
		}
	}
	if len(req.ContractProofsChecks) == 0 {
		s.logger.Warn("agent returned no storage proof checks", "agent_id", req.AgentID, "task_id", req.TaskID)
	}
	if err := s.repository.UpdateStatuses(ctx); err != nil {
		return err
	}

	s.logger.Info(
		"applied agent task result",
		"agent_id", req.AgentID,
		"task_id", req.TaskID,
		"provider_ips", len(req.ProviderIPs),
		"contract_proofs_checks", len(req.ContractProofsChecks),
	)
	return nil
}
