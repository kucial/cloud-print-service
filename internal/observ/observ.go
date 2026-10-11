package observ

import "log/slog"

// Log is the structured logger services take. Aliasing it here keeps the dependency
// direction obvious: business packages log, they do not configure logging.
type Log = slog.Logger
