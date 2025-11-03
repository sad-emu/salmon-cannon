package connections

import "time"

var MaxStreamsPerConnection int32 = 500
var MaxConnectionsPerBridge int = 1
var ConnectionIdleTimeout time.Duration = 5 * time.Minute
