package learning

import (
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/trace"
)

func sanitizeLessonRecord(record LessonRecord, cfg config.LearningConfig, redactor *trace.Redactor) LessonRecord {
	r := trace.NewRedactor()
	if redactor != nil {
		r = redactor
	}
	maxChars := cfg.GetMaxLessonFieldChars()
	record.Situation = r.SanitizeString(record.Situation, maxChars)
	record.Approach = r.SanitizeString(record.Approach, maxChars)
	record.Outcome = r.SanitizeString(record.Outcome, maxChars)
	record.ErrorMessage = r.SanitizeString(record.ErrorMessage, maxChars)
	record.Correction = r.SanitizeString(record.Correction, maxChars)
	record.BetterApproach = r.SanitizeString(record.BetterApproach, maxChars)
	return record
}
