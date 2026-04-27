package agents

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"mytonprovider-backend/pkg/constants"
	v1 "mytonprovider-backend/pkg/models/api/v1"
	"mytonprovider-backend/pkg/models/db"
)

const (
	defaultTaskLimit  = 100
	cycleTTL          = 60 * time.Minute
	assignmentTimeout = 10 * time.Minute
)

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
	mu         sync.Mutex
	cycle      *cycleState
}

type cycleState struct {
	id          string
	startedAt   time.Time
	contracts   []db.ContractToProviderRelation
	assigned    map[string]assignment
	tasks       map[string][]string
	attemptedBy map[string]map[string]struct{}
	positive    map[string]struct{}
}

type assignment struct {
	agentID    string
	assignedAt time.Time
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

	now := time.Now()
	if s.needsNewCycle(now) {
		contracts, cErr := s.repository.GetStorageContracts(ctx)
		if cErr != nil {
			return resp, cErr
		}

		s.startCycle(contracts, now)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cycle == nil || len(s.cycle.contracts) == 0 {
		return resp, nil
	}

	s.expireAssignmentsLocked(now)

	contracts := make([]db.ContractToProviderRelation, 0, limit)
	contractKeys := make([]string, 0, limit)
	for _, contract := range s.cycle.contracts {
		key := contractKey(contract.Address, contract.ProviderAddress)
		if _, ok := s.cycle.positive[key]; ok {
			continue
		}
		if _, ok := s.cycle.assigned[key]; ok {
			continue
		}
		if s.agentTriedLocked(key, req.AgentID) {
			continue
		}

		s.cycle.assigned[key] = assignment{agentID: req.AgentID, assignedAt: now}
		if s.cycle.attemptedBy[key] == nil {
			s.cycle.attemptedBy[key] = make(map[string]struct{})
		}
		s.cycle.attemptedBy[key][req.AgentID] = struct{}{}
		contracts = append(contracts, contract)
		contractKeys = append(contractKeys, key)
		if len(contracts) == limit {
			break
		}
	}

	if len(contracts) == 0 {
		s.logger.Debug("no agent tasks available", "agent_id", req.AgentID, "cycle_id", s.cycle.id)
		return resp, nil
	}

	taskID := fmt.Sprintf("%s-%s-storage-proof-%d-%d", s.cycle.id, req.AgentID, now.UnixNano(), len(contracts))
	s.cycle.tasks[taskID] = contractKeys

	resp.Tasks = []v1.AgentTask{{
		ID:        taskID,
		Kind:      v1.AgentTaskKindStorageProofCheck,
		Contracts: contracts,
	}}

	s.logger.Debug("issued agent tasks", "agent_id", req.AgentID, "cycle_id", s.cycle.id, "tasks", len(resp.Tasks), "contracts", len(contracts))
	return resp, nil
}

func (s *service) SubmitResult(ctx context.Context, req v1.AgentTaskResultRequest) error {
	if req.Error != "" {
		s.logger.Error("agent task failed", "agent_id", req.AgentID, "task_id", req.TaskID, "kind", req.Kind, "error", req.Error)
		s.releaseTask(req.AgentID, req.TaskID)
		return nil
	}

	if req.Kind != v1.AgentTaskKindStorageProofCheck {
		return fmt.Errorf("unsupported agent task result kind: %s", req.Kind)
	}

	if err := s.repository.UpdateProvidersIPs(ctx, req.ProviderIPs); err != nil {
		return err
	}

	positiveChecks := make([]db.ContractProofsCheck, 0, len(req.ContractProofsChecks))
	negativeChecks := 0
	for _, check := range req.ContractProofsChecks {
		if check.Reason == constants.ValidStorageProof {
			positiveChecks = append(positiveChecks, check)
			continue
		}

		negativeChecks++
		s.logger.Warn(
			"agent returned negative storage proof check",
			"agent_id", req.AgentID,
			"task_id", req.TaskID,
			"contract_address", check.ContractAddress,
			"provider_address", check.ProviderAddress,
			"reason", check.Reason,
		)
	}

	if len(positiveChecks) > 0 {
		if err := s.repository.UpdateContractProofsChecks(ctx, positiveChecks); err != nil {
			return err
		}
		if err := s.repository.UpdateStatuses(ctx); err != nil {
			return err
		}
	}
	if len(req.ContractProofsChecks) == 0 {
		s.logger.Warn("agent returned no storage proof checks", "agent_id", req.AgentID, "task_id", req.TaskID)
	}

	s.applyTaskResult(req.AgentID, req.TaskID, req.ContractProofsChecks, positiveChecks)

	s.logger.Info(
		"applied agent task result",
		"agent_id", req.AgentID,
		"task_id", req.TaskID,
		"provider_ips", len(req.ProviderIPs),
		"contract_proofs_checks", len(req.ContractProofsChecks),
		"positive_contract_proofs_checks", len(positiveChecks),
		"negative_contract_proofs_checks", negativeChecks,
	)
	return nil
}

func (s *service) needsNewCycle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cycle == nil || now.Sub(s.cycle.startedAt) > cycleTTL || s.allPositiveLocked()
}

func (s *service) startCycle(contracts []db.ContractToProviderRelation, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cycle != nil && now.Sub(s.cycle.startedAt) <= cycleTTL && !s.allPositiveLocked() {
		return
	}

	s.cycle = &cycleState{
		id:          fmt.Sprintf("cycle-%d", now.UnixNano()),
		startedAt:   now,
		contracts:   contracts,
		assigned:    make(map[string]assignment),
		tasks:       make(map[string][]string),
		attemptedBy: make(map[string]map[string]struct{}),
		positive:    make(map[string]struct{}),
	}
	s.logger.Info("started agent task cycle", "cycle_id", s.cycle.id, "contracts", len(contracts))
}

func (s *service) expireAssignmentsLocked(now time.Time) {
	for key, assignment := range s.cycle.assigned {
		if now.Sub(assignment.assignedAt) <= assignmentTimeout {
			continue
		}

		delete(s.cycle.assigned, key)
		s.logger.Warn("expired agent task assignment", "cycle_id", s.cycle.id, "agent_id", assignment.agentID, "contract", key)
	}
}

func (s *service) agentTriedLocked(key, agentID string) bool {
	agents, ok := s.cycle.attemptedBy[key]
	if !ok {
		return false
	}
	_, ok = agents[agentID]
	return ok
}

func (s *service) allPositiveLocked() bool {
	if s.cycle == nil || len(s.cycle.contracts) == 0 {
		return false
	}

	for _, contract := range s.cycle.contracts {
		if _, ok := s.cycle.positive[contractKey(contract.Address, contract.ProviderAddress)]; !ok {
			return false
		}
	}
	return true
}

func (s *service) applyTaskResult(agentID, taskID string, checks, positiveChecks []db.ContractProofsCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cycle == nil {
		return
	}
	s.releaseTaskLocked(agentID, taskID, checks)

	for _, check := range positiveChecks {
		s.cycle.positive[contractKey(check.ContractAddress, check.ProviderAddress)] = struct{}{}
	}
}

func (s *service) releaseTask(agentID, taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cycle == nil {
		return
	}
	s.releaseTaskLocked(agentID, taskID, nil)
}

func (s *service) releaseTaskLocked(agentID, taskID string, checks []db.ContractProofsCheck) {
	if taskID != "" {
		keys, ok := s.cycle.tasks[taskID]
		if ok {
			for _, key := range keys {
				if a, ok := s.cycle.assigned[key]; ok && a.agentID == agentID {
					delete(s.cycle.assigned, key)
				}
			}
			delete(s.cycle.tasks, taskID)
			return
		}
	}

	for _, check := range checks {
		key := contractKey(check.ContractAddress, check.ProviderAddress)
		if a, ok := s.cycle.assigned[key]; ok && a.agentID == agentID {
			delete(s.cycle.assigned, key)
		}
	}
}

func contractKey(contractAddress, providerAddress string) string {
	return contractAddress + "/" + providerAddress
}
