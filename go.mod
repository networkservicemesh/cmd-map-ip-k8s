module github.com/networkservicemesh/cmd-map-ip-k8s

go 1.16

require (
	github.com/antonfisher/nested-logrus-formatter v1.3.1
	github.com/edwarnicke/serialize v1.0.7
	github.com/kelseyhightower/envconfig v1.4.0
	github.com/networkservicemesh/sdk v0.5.1-0.20220506141607-3a426c33c317
	github.com/sirupsen/logrus v1.8.1
	github.com/stretchr/testify v1.7.0
	go.uber.org/goleak v1.1.12
	google.golang.org/genproto v0.0.0-20211129164237-f09f9a12af12 // indirect
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.21.1
	k8s.io/apimachinery v0.21.1
	k8s.io/client-go v0.21.1
	k8s.io/klog/v2 v2.40.1 // indirect
)
