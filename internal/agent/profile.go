package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

type Profile struct {
	Name         string
	Version      int
	Prompt       string
	Capabilities domain.Policy
	Contract     string
}

func Role(name string) (Profile, error) {
	read := domain.Policy{Tools: []string{"read", "request_input"}}
	write := domain.Policy{Tools: []string{"read", "write", "edit", "bash", "create_task", "request_input"}}
	p := Profile{Name: name, Version: 1, Capabilities: read}
	switch name {
	case "executor":
		p.Capabilities = write
		p.Prompt = "Complete the goal using evidence from the workspace. Use request_input for missing requirements and create_task only for independently verifiable subgoals. Never claim a tool succeeded without its result."
	case "responder":
		p.Prompt = "Answer the user's question using available evidence. Do not modify files or execute shell commands."
	case "reviewer":
		p.Prompt = "Review without making changes. Return JSON {\"summary\":string,\"findings\":[{\"file\":string,\"line\":number,\"description\":string}]}. Every finding must cite evidence."
		p.Contract = "review"
	case "planner":
		p.Prompt = "Clarify missing requirements with request_input. Return JSON {\"goal\":string,\"steps\":[{\"id\":string,\"description\":string,\"dependencies\":[string]}]}. Do not execute the plan."
		p.Contract = "plan"
	case "verifier":
		p.Prompt = "Independently evaluate the goal against provided changes and tool evidence. Return JSON {\"status\":\"passed\"|\"failed\"|\"inconclusive\",\"summary\":string,\"evidence\":[string]}. Passed requires concrete acceptance evidence, never just the executor's claim. You cannot execute tests yourself."
		p.Contract = "verification"
	case "validator":
		p.Capabilities = domain.Policy{Tools: []string{"read", "bash", "request_input"}}
		p.Prompt = "Validate the changes against the goal. Tests execute arbitrary project code and need tool authorization. Execute relevant checks and report their exit codes and evidence. Do not edit source files."
	case "repair":
		p.Capabilities = write
		p.Prompt = "Repair only the failed acceptance criteria using the supplied verification evidence. Preserve the original goal. Recheck changes and describe actual results."
	case "critic-correctness":
		p.Prompt = "Critique the proposed answer independently for correctness. Cite specific contradictory evidence; do not modify files."
	case "critic-security":
		p.Prompt = "Critique the proposed answer independently for security and edge cases. Cite specific evidence; do not run shell commands."
	case "judge":
		p.Prompt = "Synthesize the original answer and independent critiques in their given order. Resolve contradictions using evidence and disclose uncertainty."
	case "terminal":
		p.Capabilities = domain.Policy{Tools: []string{"bash", "request_input"}}
		p.Prompt = "Fulfil the user's terminal operation through the bash tool. Do not claim commands ran without tool results. Commands require explicit authorization."
	default:
		return Profile{}, fmt.Errorf("unknown role %q", name)
	}
	return p, nil
}

type Step struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	Dependencies []string `json:"dependencies"`
}

type Plan struct {
	Goal  string `json:"goal"`
	Steps []Step `json:"steps"`
}

type Verification struct {
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
}

type Finding struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
}

type Review struct {
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

func decodeResult(text string, v any) error {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
	}
	if err := model.StrictJSON([]byte(text)); err != nil {
		return fmt.Errorf("invalid structured result: %w", err)
	}
	d := json.NewDecoder(bytes.NewBufferString(text))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid structured result: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("structured result contains trailing data")
	}
	return nil
}

func ValidateResult(profile Profile, text string) error {
	switch profile.Contract {
	case "verification":
		_, err := ParseVerification(text)
		return err
	case "plan":
		var p Plan
		if err := decodeResult(text, &p); err != nil {
			return err
		}
		if p.Goal == "" || len(p.Steps) == 0 || len(p.Steps) > 32 {
			return fmt.Errorf("plan requires a goal and 1..32 steps")
		}
		steps := make(map[string]Step, len(p.Steps))
		for _, s := range p.Steps {
			if s.ID == "" || s.Description == "" {
				return fmt.Errorf("plan step missing identity or description")
			}
			if _, ok := steps[s.ID]; ok {
				return fmt.Errorf("duplicate plan step %s", s.ID)
			}
			steps[s.ID] = s
		}
		seen := map[string]int{}
		var visit func(string) error
		visit = func(id string) error {
			s, ok := steps[id]
			if !ok {
				return fmt.Errorf("unknown dependency %s", id)
			}
			if seen[id] == 1 {
				return fmt.Errorf("cyclic plan dependency %s", id)
			}
			if seen[id] == 2 {
				return nil
			}
			seen[id] = 1
			for _, dep := range s.Dependencies {
				if err := visit(dep); err != nil {
					return err
				}
			}
			seen[id] = 2
			return nil
		}
		for id := range steps {
			if err := visit(id); err != nil {
				return err
			}
		}
	case "review":
		var r Review
		if err := decodeResult(text, &r); err != nil {
			return err
		}
		if r.Summary == "" {
			return fmt.Errorf("review summary required")
		}
		for _, f := range r.Findings {
			if f.File == "" || f.Line < 1 || f.Description == "" {
				return fmt.Errorf("review finding lacks source evidence")
			}
		}
	default:
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("empty role result")
		}
	}
	return nil
}

func ParseVerification(text string) (Verification, error) {
	var v Verification
	if err := decodeResult(text, &v); err != nil {
		return v, err
	}
	if v.Status != "passed" && v.Status != "failed" && v.Status != "inconclusive" {
		return v, fmt.Errorf("invalid verification status %q", v.Status)
	}
	if v.Summary == "" || (v.Status == "passed" && len(v.Evidence) == 0) {
		return v, fmt.Errorf("verification lacks summary or evidence")
	}
	return v, nil
}
