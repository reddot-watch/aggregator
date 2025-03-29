package config

// Constants defining default values for application configuration
const (
	DefaultFeedsCSVPath = "./feeds.csv"
	DefaultDBPath       = "./feeds.db"

	RemoteFeedsURL = "https://raw.githubusercontent.com/reddot-watch/curated-world-news/main/feeds.csv"

	DefaultServerPort = 8080
	DefaultServerHost = "" // Empty string means all interfaces

	DefaultWorkerCount   = 0  // 0 means use runtime.NumCPU()
	DefaultInterval      = 15 // Minutes between processing runs
	DefaultRetentionDays = 3  // Days to keep items before purging

	DefaultLogLevel = "debug"
)
