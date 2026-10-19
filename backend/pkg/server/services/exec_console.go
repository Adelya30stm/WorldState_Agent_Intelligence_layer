package services

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pentagi/pkg/server/logger"
	"pentagi/pkg/server/response"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
)

// ─── Types ──────────────────────────────────────────────────────────────────

type ExecRequest struct {
	Command string `json:"command"`
}

type ExecResponse struct {
	FlowID    int64  `json:"flowId"`
	Container string `json:"container"`
	Command   string `json:"command"`
	Output    string `json:"output"`
	ExitCode  int    `json:"exitCode"`
}

// ─── Service ──────────────────────────────────────────────────────────────────

// ExecService runs one-shot shell commands inside a flow's Kali terminal container.
// It talks to the Docker daemon directly via the mounted docker.sock, resolving the
// target container from the flow's row in the containers table.
type ExecService struct {
	db     *gorm.DB
	docker *client.Client
}

func NewExecService(db *gorm.DB) *ExecService {
	// Best-effort: if the docker client can't be created, exec requests will 500.
	dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		dc = nil
	}
	return &ExecService{db: db, docker: dc}
}

const (
	execTimeout   = 120 * time.Second
	execOutputCap = 200_000 // bytes of combined stdout/stderr returned to the UI
)

type containerRow struct {
	Name    string `gorm:"column:name"`
	LocalID string `gorm:"column:local_id"`
}

// resolveContainer picks the running terminal container for a flow. It prefers the
// docker container id (local_id); falls back to the conventional name.
func (s *ExecService) resolveContainer(flowID int64) (ref, name string) {
	var rows []containerRow
	s.db.Raw(`
		SELECT name, COALESCE(local_id, '') AS local_id
		FROM containers
		WHERE flow_id = ? AND status = 'running'
		ORDER BY created_at DESC
	`, flowID).Scan(&rows)

	for _, r := range rows {
		if strings.Contains(r.Name, "-terminal-") {
			if r.LocalID != "" {
				return r.LocalID, r.Name
			}
			return r.Name, r.Name
		}
	}
	if len(rows) > 0 {
		if rows[0].LocalID != "" {
			return rows[0].LocalID, rows[0].Name
		}
		return rows[0].Name, rows[0].Name
	}
	// Fallback to the conventional name (redscope-terminal-<flowID>).
	fallback := fmt.Sprintf("redscope-terminal-%d", flowID)
	return fallback, fallback
}

// RunCommand executes a shell command inside the flow's terminal container.
//
// @Summary Run a shell command in a flow's Kali container
// @Tags exec
// @Accept json
// @Produce json
// @Param flowID path int true "Flow ID"
// @Param request body ExecRequest true "Command to run"
// @Success 200 {object} ExecResponse
// @Failure 400 {object} response.Error
// @Failure 500 {object} response.Error
// @Router /flows/{flowID}/exec [post]
func (s *ExecService) RunCommand(c *gin.Context) {
	log := logger.FromContext(c)

	flowID, err := strconv.ParseInt(c.Param("flowID"), 10, 64)
	if err != nil {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return
	}

	var req ExecRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, response.ErrExecInvalidRequest, err)
		return
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		response.Error(c, response.ErrExecInvalidRequest, fmt.Errorf("command is empty"))
		return
	}

	if s.docker == nil {
		response.Error(c, response.ErrInternal, fmt.Errorf("docker client unavailable"))
		return
	}

	ref, name := s.resolveContainer(flowID)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), execTimeout)
	defer cancel()

	execID, err := s.docker.ContainerExecCreate(ctx, ref, container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-lc", cmd},
		WorkingDir:   "/work",
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		log.WithError(err).Errorf("exec create failed for container %s (flow %d)", name, flowID)
		response.Error(c, response.ErrInternal, fmt.Errorf("failed to start command (is the flow container running?): %w", err))
		return
	}

	attach, err := s.docker.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		log.WithError(err).Errorf("exec attach failed for container %s", name)
		response.Error(c, response.ErrInternal, fmt.Errorf("failed to attach to command: %w", err))
		return
	}
	defer attach.Close()

	var stdout, stderr bytes.Buffer
	// StdCopy demultiplexes the docker stream into stdout/stderr.
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		log.WithError(err).Warn("error reading exec output")
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += stderr.String()
	}
	if len(output) > execOutputCap {
		output = output[:execOutputCap] + "\n...[output truncated]"
	}

	exitCode := 0
	if inspect, err := s.docker.ContainerExecInspect(ctx, execID.ID); err == nil {
		exitCode = inspect.ExitCode
	}

	response.Success(c, http.StatusOK, ExecResponse{
		FlowID:    flowID,
		Container: name,
		Command:   cmd,
		Output:    output,
		ExitCode:  exitCode,
	})
}
