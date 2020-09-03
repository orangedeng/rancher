$ErrorActionPreference = 'Stop'

$env:GO111MODULE="on"

$REQUIRE=@("github.com/vgough/grpc-proxy@v0.0.0-20191207203309-13d1aa04a5a6",
"github.com/aliyun/alibaba-cloud-sdk-go@v1.61.27")

$REPLACE=@("github.com/Azure/azure-sdk-for-go=github.com/Azure/azure-sdk-for-go@v36.2.0+incompatible",
"github.com/rancher/types=github.com/cnrancher/pandaria-types@v0.0.0-20200903044205-b928571d0139",
"github.com/rancher/kontainer-engine=github.com/cnrancher/kontainer-engine@v0.0.4-dev.0.20200903044801-e5debdafef8b")

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
