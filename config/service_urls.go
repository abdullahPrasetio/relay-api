package config

type ServiceURLs struct {
	UserURL  string `mapstructure:"user_url"`
	OrderURL string `mapstructure:"order_url"`
}
