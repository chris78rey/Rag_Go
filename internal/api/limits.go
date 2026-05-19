package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codex/semantic-rag-go/internal/auth"
)

var errRateLimitExceeded = errors.New("rate limit exceeded")

type planConfig struct {
	Code                 string
	MaxUploadMB          int
	DailyQueryLimit      *int
	RepositoryLimit      int
	MaxTotalStorageMB    int
	QueriesPerMinute     int
	UploadsPerMinute     int
	MaxConcurrentUploads int
}

func (p planConfig) maxUploadBytes() int64 {
	return int64(p.MaxUploadMB) << 20
}

func (p planConfig) maxStorageBytes() int64 {
	return int64(p.MaxTotalStorageMB) << 20
}

func (s *Server) loadPlanConfig(ctx context.Context, planCode string) (planConfig, error) {
	normalized := auth.NormalizePlanCode(planCode)
	cfg := fallbackPlanConfig(normalized)

	var maxUploadMB, repoLimit, storageMB, queriesPerMinute, uploadsPerMinute, concurrentUploads int64
	var dailyLimit sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT p.code, p.max_upload_mb, p.daily_query_limit,
		       COALESCE(l.max_repositories, CASE p.code WHEN 'premium' THEN 10 ELSE 3 END),
		       COALESCE(l.max_total_storage_mb, CASE p.code WHEN 'premium' THEN 1500 ELSE 150 END),
		       COALESCE(l.queries_per_minute, CASE p.code WHEN 'premium' THEN 20 ELSE 6 END),
		       COALESCE(l.uploads_per_minute, CASE p.code WHEN 'premium' THEN 4 ELSE 2 END),
		       COALESCE(l.max_concurrent_uploads, CASE p.code WHEN 'premium' THEN 3 ELSE 1 END)
		FROM plans p
		LEFT JOIN plan_limits l ON l.plan_code = p.code
		WHERE p.code = ?`,
		normalized,
	).Scan(
		&cfg.Code,
		&maxUploadMB,
		&dailyLimit,
		&repoLimit,
		&storageMB,
		&queriesPerMinute,
		&uploadsPerMinute,
		&concurrentUploads,
	)
	if err != nil {
		if isMissingTableError(err, "plans") || isMissingTableError(err, "plan_limits") {
			return cfg, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("cargando plan %s: %w", normalized, err)
	}

	cfg.MaxUploadMB = int(maxUploadMB)
	cfg.RepositoryLimit = int(repoLimit)
	cfg.MaxTotalStorageMB = int(storageMB)
	cfg.QueriesPerMinute = int(queriesPerMinute)
	cfg.UploadsPerMinute = int(uploadsPerMinute)
	cfg.MaxConcurrentUploads = int(concurrentUploads)
	if dailyLimit.Valid {
		value := int(dailyLimit.Int64)
		cfg.DailyQueryLimit = &value
	} else {
		cfg.DailyQueryLimit = nil
	}

	if cfg.MaxUploadMB <= 0 {
		cfg.MaxUploadMB = fallbackPlanConfig(normalized).MaxUploadMB
	}
	if cfg.RepositoryLimit <= 0 {
		cfg.RepositoryLimit = fallbackPlanConfig(normalized).RepositoryLimit
	}
	if cfg.MaxTotalStorageMB <= 0 {
		cfg.MaxTotalStorageMB = fallbackPlanConfig(normalized).MaxTotalStorageMB
	}
	if cfg.QueriesPerMinute <= 0 {
		cfg.QueriesPerMinute = fallbackPlanConfig(normalized).QueriesPerMinute
	}
	if cfg.UploadsPerMinute <= 0 {
		cfg.UploadsPerMinute = fallbackPlanConfig(normalized).UploadsPerMinute
	}
	if cfg.MaxConcurrentUploads <= 0 {
		cfg.MaxConcurrentUploads = fallbackPlanConfig(normalized).MaxConcurrentUploads
	}

	return cfg, nil
}

func fallbackPlanConfig(planCode string) planConfig {
	normalized := auth.NormalizePlanCode(planCode)
	switch normalized {
	case auth.PlanCodePremium:
		return planConfig{
			Code:                 auth.PlanCodePremium,
			MaxUploadMB:          150,
			DailyQueryLimit:      nil,
			RepositoryLimit:      10,
			MaxTotalStorageMB:    1500,
			QueriesPerMinute:     20,
			UploadsPerMinute:     4,
			MaxConcurrentUploads: 3,
		}
	default:
		limit := 50
		return planConfig{
			Code:                 auth.PlanCodeNormal,
			MaxUploadMB:          50,
			DailyQueryLimit:      &limit,
			RepositoryLimit:      3,
			MaxTotalStorageMB:    150,
			QueriesPerMinute:     6,
			UploadsPerMinute:     2,
			MaxConcurrentUploads: 1,
		}
	}
}

func (s *Server) countDocuments(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM documents WHERE user_id = ?`,
		userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("contando documentos: %w", err)
	}
	return count, nil
}

func (s *Server) sumStorageBytes(ctx context.Context, userID string) (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0) FROM documents WHERE user_id = ?`,
		userID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sumando almacenamiento: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func (s *Server) countProcessingDocuments(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM documents WHERE user_id = ? AND status = 'processing'`,
		userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("contando procesos en curso: %w", err)
	}
	return count, nil
}

func (s *Server) enforceRateLimit(ctx context.Context, userID, action string, limit int) error {
	if limit <= 0 {
		return nil
	}

	windowKey := time.Now().UTC().Format("2006-01-02T15:04")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("iniciando control de ritmo: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO request_rate_limits (user_id, action, window_key, request_count)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(user_id, action, window_key)
		DO UPDATE SET request_count = request_count + 1
	`, userID, action, windowKey)
	if err != nil {
		tx.Rollback()
		if isMissingTableError(err, "request_rate_limits") {
			return nil
		}
		return fmt.Errorf("registrando ritmo: %w", err)
	}

	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT request_count
		 FROM request_rate_limits
		 WHERE user_id = ? AND action = ? AND window_key = ?`,
		userID, action, windowKey,
	).Scan(&count); err != nil {
		tx.Rollback()
		if isMissingTableError(err, "request_rate_limits") {
			return nil
		}
		return fmt.Errorf("leyendo ritmo: %w", err)
	}

	if count > limit {
		tx.Rollback()
		return errRateLimitExceeded
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("confirmando ritmo: %w", err)
	}

	return nil
}

func isMissingTableError(err error, table string) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	table = strings.ToLower(table)
	return strings.Contains(lower, "no such table") && strings.Contains(lower, table)
}
