package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"gobooks/internal/config"
)

type TaskType string
type Capability string
type RiskLevel string

const (
	TaskSmartPickerLearningSummary       TaskType = "smartpicker_learning_summary"
	TaskSmartPickerAliasSuggestion       TaskType = "smartpicker_alias_suggestion"
	TaskSmartPickerRankingHintGeneration TaskType = "smartpicker_ranking_hint_generation"
	TaskReceiptOCRExtract                TaskType = "receipt_ocr_extract"
	TaskInvoiceFieldExtract              TaskType = "invoice_field_extract"
	TaskBankMemoParse                    TaskType = "bank_memo_parse"
	TaskAccountingCommandParse           TaskType = "accounting_command_parse"
	TaskFinancialInsightSummary          TaskType = "financial_insight_summary"
	TaskAnomalyExplanation               TaskType = "anomaly_explanation"
	TaskEmailDraftGeneration             TaskType = "email_draft_generation"

	CapabilityCheapClassification Capability = "cheap_classification"
	CapabilitySummarization       Capability = "summarization"
	CapabilityStructuredOutput    Capability = "structured_output"
	CapabilityTextReasoning       Capability = "text_reasoning"
	CapabilityVisionOCR           Capability = "vision_ocr"
	CapabilityEmbedding           Capability = "embedding"
	CapabilityReranking           Capability = "reranking"
	CapabilityToolCalling         Capability = "tool_calling"

	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"

	GatewayStatusSkipped       = "skipped"
	GatewayStatusSucceeded     = "succeeded"
	GatewayStatusFailed        = "failed"
	GatewayStatusInvalidOutput = "invalid_output"
)

type StructuredTaskRequest struct {
	CompanyID             uint
	JobRunID              string
	TaskType              TaskType
	Capability            Capability
	InputJSON             string
	RequestSchemaVersion  string
	ResponseSchemaVersion string
	PromptVersion         string
	RiskLevel             RiskLevel
}

type StructuredTaskResponse struct {
	Status           string
	Provider         string
	Model            string
	OutputJSON       string
	ErrorMessage     string
	TokenInputCount  int
	TokenOutputCount int
	EstimatedCost    float64
}

type ModelSelection struct {
	Provider string
	Model    string
}

type AIProvider interface {
	Name() string
	Supports(taskType TaskType, capability Capability) bool
	CompleteStructured(ctx context.Context, req StructuredTaskRequest, model ModelSelection) (StructuredTaskResponse, error)
}

type NoopAIProvider struct{}

func (NoopAIProvider) Name() string { return "noop" }

func (NoopAIProvider) Supports(TaskType, Capability) bool { return true }

func (NoopAIProvider) CompleteStructured(_ context.Context, req StructuredTaskRequest, model ModelSelection) (StructuredTaskResponse, error) {
	return StructuredTaskResponse{
		Status:       GatewayStatusSkipped,
		Provider:     "noop",
		Model:        model.Model,
		OutputJSON:   "{}",
		ErrorMessage: "AI gateway disabled or no provider configured",
	}, nil
}

type ModelRouter struct {
	cfg config.Config
}

func NewModelRouter(cfg config.Config) ModelRouter {
	return ModelRouter{cfg: cfg}
}

func (r ModelRouter) SelectModel(_ context.Context, taskType TaskType, _ uint, _ RiskLevel) ModelSelection {
	provider := r.cfg.AIDefaultProvider
	model := r.cfg.AIDefaultCheapModel
	switch taskType {
	case TaskReceiptOCRExtract:
		model = firstNonEmptyModel(r.cfg.AIDefaultVisionModel, r.cfg.AIDefaultAdvancedModel, r.cfg.AIDefaultCheapModel)
	case TaskAccountingCommandParse, TaskFinancialInsightSummary, TaskAnomalyExplanation:
		model = firstNonEmptyModel(r.cfg.AIDefaultAdvancedModel, r.cfg.AIDefaultCheapModel)
	default:
		model = firstNonEmptyModel(r.cfg.AIDefaultCheapModel, r.cfg.AIDefaultAdvancedModel)
	}
	return ModelSelection{Provider: provider, Model: model}
}

type PromptRegistry struct {
	prompts map[TaskType]string
}

func NewPromptRegistry() PromptRegistry {
	return PromptRegistry{prompts: map[TaskType]string{
		TaskSmartPickerLearningSummary:       "smartpicker_learning_summary.v1",
		TaskSmartPickerAliasSuggestion:       "smartpicker_alias_suggestion.v1",
		TaskSmartPickerRankingHintGeneration: "smartpicker_ranking_hint_generation.v1",
		TaskAccountingCommandParse:           "accounting_command_parse.v1",
	}}
}

func (r PromptRegistry) GetPrompt(taskType TaskType, version string) string {
	if version != "" {
		return string(taskType) + "." + version
	}
	return r.prompts[taskType]
}

type StructuredOutputValidator struct{}

func (StructuredOutputValidator) Validate(taskType TaskType, outputJSON string) error {
	if outputJSON == "" {
		return fmt.Errorf("%s output is empty", taskType)
	}
	var raw any
	if err := json.Unmarshal([]byte(outputJSON), &raw); err != nil {
		return fmt.Errorf("%s output is not valid JSON: %w", taskType, err)
	}
	return nil
}

type Gateway struct {
	cfg       config.Config
	router    ModelRouter
	provider  AIProvider
	validator StructuredOutputValidator
}

func NewGateway(cfg config.Config, provider AIProvider) Gateway {
	if provider == nil {
		provider = NoopAIProvider{}
	}
	return Gateway{
		cfg:       cfg,
		router:    NewModelRouter(cfg),
		provider:  provider,
		validator: StructuredOutputValidator{},
	}
}

func (g Gateway) RunStructuredTask(ctx context.Context, req StructuredTaskRequest) (StructuredTaskResponse, error) {
	model := g.router.SelectModel(ctx, req.TaskType, req.CompanyID, req.RiskLevel)
	if !g.cfg.AIGatewayEnabled || g.provider == nil || !g.provider.Supports(req.TaskType, req.Capability) {
		return NoopAIProvider{}.CompleteStructured(ctx, req, model)
	}
	resp, err := g.provider.CompleteStructured(ctx, req, model)
	if err != nil {
		resp.Status = GatewayStatusFailed
		resp.Provider = g.provider.Name()
		resp.Model = model.Model
		resp.ErrorMessage = err.Error()
		return resp, err
	}
	if resp.Status == GatewayStatusSucceeded {
		if err := g.validator.Validate(req.TaskType, resp.OutputJSON); err != nil {
			resp.Status = GatewayStatusInvalidOutput
			resp.ErrorMessage = err.Error()
			return resp, err
		}
	}
	return resp, nil
}

func firstNonEmptyModel(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
