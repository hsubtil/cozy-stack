module github.com/cozy/cozy-stack

go 1.12

require (
	github.com/Masterminds/semver v1.4.2
	github.com/appleboy/go-fcm v0.1.4
	github.com/cozy/goexif2 v0.0.0-20180125141006-830968571cff
	github.com/cozy/gomail v0.0.0-20170313100128-1395d9a6a6c0
	github.com/cozy/httpcache v0.0.0-20180914105234-d3dc4988de66
	github.com/dhowden/tag v0.0.0-20190519100835-db0c67e351b1
	github.com/dustin/go-humanize v1.0.0
	github.com/emersion/go-vcard v0.0.0-20190105225839-8856043f13c5
	github.com/go-redis/redis v6.15.2+incompatible
	github.com/gofrs/uuid v3.2.0+incompatible
	github.com/golang/gddo v0.0.0-20190818233415-287de01127ef
	github.com/google/go-querystring v1.0.0
	github.com/google/gops v0.3.6
	github.com/gorilla/websocket v1.4.0
	github.com/h2non/filetype v1.0.10
	github.com/hashicorp/go-multierror v1.0.0
	github.com/howeyc/gopass v0.0.0-20170109162249-bf9dde6d0d2c
	github.com/justincampbell/bigduration v0.0.0-20160531141349-e45bf03c0666
	github.com/konsorten/go-windows-terminal-sequences v1.0.2 // indirect
	github.com/labstack/echo/v4 v4.1.6
	github.com/leonelquinteros/gotext v1.4.0
	github.com/magiconair/properties v1.8.1 // indirect
	github.com/mitchellh/mapstructure v1.1.2
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/mssola/user_agent v0.5.0
	github.com/ncw/swift v1.0.49
	github.com/nightlyone/lockfile v0.0.0-20180618180623-0ad87eef1443
	github.com/onsi/ginkgo v1.8.0 // indirect
	github.com/onsi/gomega v1.5.0 // indirect
	github.com/oschwald/maxminddb-golang v1.3.1
	github.com/pelletier/go-toml v1.4.0 // indirect
	github.com/pkg/errors v0.8.1
	github.com/pquerna/otp v1.2.0
	github.com/prometheus/client_golang v1.1.0
	github.com/robfig/cron/v3 v3.0.0
	github.com/sideshow/apns2 v0.18.0
	github.com/sirupsen/logrus v1.4.2
	github.com/spf13/afero v1.2.2
	github.com/spf13/cobra v0.0.5
	github.com/spf13/jwalterweatherman v1.1.0 // indirect
	github.com/spf13/viper v1.4.0
	github.com/stretchr/testify v1.4.0
	golang.org/x/crypto v0.0.0-20190701094942-4def268fd1a4
	golang.org/x/image v0.0.0-20190804224331-cff245a6509b
	golang.org/x/net v0.0.0-20190819082215-74dc4d7220e7 // indirect
	golang.org/x/oauth2 v0.0.0-20190604053449-0f29369cfe45
	golang.org/x/sys v0.0.0-20190813073005-fde4db37ae7a // indirect
	golang.org/x/tools v0.0.0-20190819081747-6889da9d5479
	google.golang.org/appengine v1.6.1 // indirect
	gopkg.in/alexcesaro/quotedprintable.v3 v3.0.0-20150716171945-2caba252f4dc // indirect
	gopkg.in/dgrijalva/jwt-go.v3 v3.2.0
)

replace github.com/spf13/afero => github.com/cozy/afero v1.2.3
