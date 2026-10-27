package sqlite

import (
	"context"
	"fmt"
)

// Ready checks live persistence and the complete embedded migration history.
// It deliberately performs no external identity-provider request.
func (s *Store) Ready(ctx context.Context) error {
	if err := s.write.PingContext(ctx); err != nil {
		return err
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	version, err := migrationVersion(ctx, s.read, migrations)
	if err != nil {
		return err
	}
	if version != len(migrations) {
		return fmt.Errorf("database migrations are incomplete")
	}
	return nil
}
