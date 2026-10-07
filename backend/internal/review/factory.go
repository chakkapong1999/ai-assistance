package review

import (
	"fmt"

	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
)

// New builds the Reviewer selected by REVIEWER_MODE.
func New(cfg config.Config) (Reviewer, error) {
	switch cfg.ReviewerMode {
	case config.ReviewerMock:
		return Mock{Scenario: cfg.MockReviewScenario, Delay: cfg.MockReviewDelay}, nil
	case config.ReviewerClaudeCLI:
		return NewClaudeCLI(CLIOptions{Bin: cfg.ClaudeBin}), nil
	}
	return nil, fmt.Errorf("review: unknown reviewer mode %q", cfg.ReviewerMode)
}

// ModelName is what gets stored in reviews.model for a reviewer mode.
func ModelName(mode string) string {
	if mode == config.ReviewerMock {
		return ModelMock
	}
	return ModelClaudeCLI
}
