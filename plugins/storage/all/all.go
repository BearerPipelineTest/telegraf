package all

import (
	// Blank imports for plugins to register themselves
	_ "github.com/influxdata/telegraf/plugins/storage/boltdb"
	_ "github.com/influxdata/telegraf/plugins/storage/jsonfile"
)
