$ErrorActionPreference = 'Stop'

$env:GO111MODULE="on"

$REQUIRE=@("github.com/vgough/grpc-proxy@v0.0.0-20191207203309-13d1aa04a5a6",
"github.com/aliyun/alibaba-cloud-sdk-go@v1.61.27",
"github.com/satori/go.uuid@v1.2.0",
"github.com/prometheus/prometheus@v2.18.2+incompatible",
"github.com/rancher/prometheus-auth/pkg/data@v0.2.0",
"github.com/rancher/prometheus-auth/pkg/prom@v0.2.0",
"github.com/tidwall/gjson@v1.6.1",
"github.com/F5Networks/k8s-bigip-ctlr@v0.0.0-20201204153954-a3df363ee660")

$REPLACE=@("github.com/Azure/azure-sdk-for-go=github.com/Azure/azure-sdk-for-go@v36.2.0+incompatible",
"github.com/rancher/types=github.com/cnrancher/pandaria-types@v0.0.0-20210304054958-15407f3e8211",
"github.com/rancher/kontainer-engine=github.com/cnrancher/kontainer-engine@v0.0.4-dev.0.20210125041539-ea6f91ce0d09",
"github.com/rancher/prometheus-auth/pkg/data=github.com/cnrancher/prometheus-auth/pkg/data@v0.0.0-20201013075525-c015fa82fdd7",
"github.com/rancher/prometheus-auth/pkg/prom=github.com/cnrancher/prometheus-auth/pkg/prom@v0.0.0-20201013075525-c015fa82fdd7",
"github.com/prometheus/prometheus=github.com/prometheus/prometheus@v0.0.0-20200626085723-c448ada63d83",
"github.com/satori/go.uuid=github.com/satori/go.uuid@v1.2.0",
"github.com/segmentio/kafka-go=github.com/segmentio/kafka-go@v0.0.0-20190411192201-218fd49cff39",
"github.com/rancher/norman=github.com/cnrancher/pandaria-norman@v0.0.0-20210315110740-ca8de0c09a03")

# https://golang.org/cmd/go/#hdr-Edit_go_mod_from_tools_or_scripts

foreach ($rq in $REQUIRE) {
   go mod edit -require $rq
}

foreach ($rp in $REPLACE) {
   go mod edit -replace $rp
}

$OAUTH_TOKEN=$env:OAUTH_TOKEN
$OURL = ('https://{0}:x-oauth-basic@github.com/' -f $OAUTH_TOKEN)

if ($OAUTH_TOKEN -ne "") {
   git config --global url.$OURL.insteadOf "https://github.com/"
}

$env:GOPRIVATE="github.com/cnrancher"
go mod vendor
