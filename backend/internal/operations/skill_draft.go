package operations

import (
	"encoding/json"

	"infinite-canvas/backend/internal/skills"
)

type SkillDrafts interface {
	DraftSkill(string, skills.SkillMutationRequest) (*skills.SkillItem, error)
}

// registerSkillDraftOp keeps the useful local draft workflow without exposing
// any SkillHub discovery, account, upload, or submission operation.
func registerSkillDraftOp(r *Registry) {
	r.Register(Op{ID: "skill.draft", AssistantCanvas: true, Scope: ScopeCanvas, Summary: "把当前画布的创作方法保存为本机私人技能草稿", Params: json.RawMessage(`{"type":"object","properties":{"canvasId":{"type":"string"},"name":{"type":"string"},"description":{"type":"string"},"instruction":{"type":"string"}},"required":["canvasId","name","description","instruction"],"additionalProperties":false}`), Handler: func(ctx *Context, raw json.RawMessage) (any, error) {
		var args struct {
			CanvasID    string `json:"canvasId"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Instruction string `json:"instruction"`
		}
		if err := decodeParams(raw, &args); err != nil {
			return nil, err
		}
		if _, err := ctx.Domain.UserCanvasProject(ctx.UserID, args.CanvasID); err != nil {
			return nil, mapDomainError(err)
		}
		port, ok := ctx.Domain.(SkillDrafts)
		if !ok {
			return nil, Unsupported("skill_unavailable", "当前工作区无法保存本机技能草稿")
		}
		created, err := port.DraftSkill(ctx.UserID, skills.SkillMutationRequest{SkillName: args.Name, Description: args.Description, Instruction: args.Instruction, Tag: "others", IsPrivate: true})
		return created, mapDomainError(err)
	}})
}
