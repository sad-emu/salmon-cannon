package connections

import "time"

var MaxStreamsPerConnection int32 = 100
var MaxConnectionsPerBridge int = 500
var ConnectionIdleTimeout time.Duration = 5 * time.Minute
