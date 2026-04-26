package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"mytonprovider-backend/pkg/agents/checker"
	v1 "mytonprovider-backend/pkg/models/api/v1"
)

type Worker struct {
	agentID        string
	coordinatorURL string
	accessToken    string
	batchSize      int
	pollInterval   time.Duration
	checker        *checker.Checker
	httpClient     *http.Client
	logger         *slog.Logger
}

func NewWorker(agentID, coordinatorURL, accessToken string, batchSize int, pollInterval time.Duration, checker *checker.Checker, logger *slog.Logger) *Worker {
	return &Worker{
		agentID:        agentID,
		coordinatorURL: strings.TrimRight(coordinatorURL, "/"),
		accessToken:    accessToken,
		batchSize:      batchSize,
		pollInterval:   pollInterval,
		checker:        checker,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		logger: logger,
	}
}

func (w *Worker) Start(ctx context.Context) error {
	if w.coordinatorURL == "" {
		return fmt.Errorf("agent coordinator URL is required")
	}
	if w.accessToken == "" {
		return fmt.Errorf("agent access token is required")
	}
	if w.batchSize <= 0 {
		w.batchSize = 100
	}
	if w.pollInterval <= 0 {
		w.pollInterval = 30 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := w.runOnce(ctx); err != nil {
			w.logger.Error("agent cycle failed", "error", err)
		}

		t := time.NewTimer(w.pollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) error {
	tasks, err := w.pollTasks(ctx)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		w.logger.Debug("no agent tasks")
		return nil
	}

	for _, task := range tasks {
		result := v1.AgentTaskResultRequest{
			AgentID: w.agentID,
			TaskID:  task.ID,
			Kind:    task.Kind,
		}

		if task.Kind != v1.AgentTaskKindStorageProofCheck {
			result.Error = fmt.Sprintf("unsupported task kind: %s", task.Kind)
			_ = w.submitResult(ctx, result)
			continue
		}

		proofResult, checkErr := w.checker.CheckStorageProofs(ctx, task.Contracts)
		if checkErr != nil {
			result.Error = checkErr.Error()
		} else {
			result.ProviderIPs = proofResult.ProviderIPs
			result.ContractProofsChecks = proofResult.ContractProofsChecks
		}

		if err := w.submitResult(ctx, result); err != nil {
			return err
		}
	}

	return nil
}

func (w *Worker) pollTasks(ctx context.Context) ([]v1.AgentTask, error) {
	var resp v1.AgentPollResponse
	err := w.post(ctx, "/api/v1/agents/tasks/poll", v1.AgentPollRequest{
		AgentID: w.agentID,
		Limit:   w.batchSize,
	}, &resp)
	if err != nil {
		return nil, err
	}

	return resp.Tasks, nil
}

func (w *Worker) submitResult(ctx context.Context, result v1.AgentTaskResultRequest) error {
	return w.post(ctx, "/api/v1/agents/tasks/result", result, nil)
}

func (w *Worker) post(ctx context.Context, path string, body any, resp any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.coordinatorURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.accessToken)

	hResp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer hResp.Body.Close()

	if hResp.StatusCode < 200 || hResp.StatusCode >= 300 {
		return fmt.Errorf("coordinator returned status %d for %s", hResp.StatusCode, path)
	}
	if resp == nil {
		return nil
	}

	return json.NewDecoder(hResp.Body).Decode(resp)
}
