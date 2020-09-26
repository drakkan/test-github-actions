#!/bin/bash

mkdir dist
cd ..
LATEST_TAG=$(git describe --tags $(git rev-list --tags --max-count=1))
NUM_COMMITS_FROM_TAG=$(git rev-list ${LATEST_TAG}.. --count)
#COMMIT_HASH=$(git rev-parse --short HEAD)
VERSION=$(echo "${LATEST_TAG}" | awk -F. -v OFS=. '{$NF++;print}')-dev.${NUM_COMMITS_FROM_TAG}

echo -n ${VERSION} > pkgs/dist/version
cd pkgs/dist
BASE_DIR="../.."

echo "SFTPGO_HTTPD__TEMPLATES_PATH=/usr/share/sftpgo/templates" > sftpgo.env
echo "SFTPGO_HTTPD__STATIC_FILES_PATH=/usr/share/sftpgo/static" >> sftpgo.env
echo "SFTPGO_HTTPD__BACKUPS_PATH=/var/lib/sftpgo/backups" >> sftpgo.env
echo "SFTPGO_DATA_PROVIDER__CREDENTIALS_PATH=/var/lib/sftpgo/credentials" >> sftpgo.env

cp ../../sftpgo.json .
sed -i 's/sftpgo.db/\/var\/lib\/sftpgo\/sftpgo.db/g' sftpgo.json
$BASE_DIR/sftpgo gen completion bash > sftpgo-completion.bash
$BASE_DIR/sftpgo gen man -d man1

cat >nfpm.yaml <<EOF
name: "sftpgo"
arch: "amd64"
platform: "linux"
version: ${VERSION}
release: 1
section: "default"
priority: "extra"
maintainer: "Nicola Murino <nicola.murino@gmail.com>"
provides:
  - sftpgo
description: |
  SFTPGo is a fully featured and highly configurable
    SFTP server with optional FTP/S and WebDAV support.
    It can serve local filesystem, S3, GCS
vendor: "SFTPGo"
homepage: "https://github.com/drakkan/sftpgo"
license: "GPL-3.0"
files:
  ${BASE_DIR}/sftpgo: "/usr/bin/sftpgo"
  ./sftpgo.env: "/etc/sftpgo/sftpgo.env"
  ./sftpgo-completion.bash: "/etc/bash_completion.d/sftpgo-completion.bash"
  ./man1/*: "/usr/share/man/man1/"
  ${BASE_DIR}/init/sftpgo.service: "/lib/systemd/system/sftpgo.service"
  ${BASE_DIR}/examples/rest-api-cli/sftpgo_api_cli.py: "/usr/bin/sftpgo_api_cli"
  ${BASE_DIR}/templates/*: "/usr/share/sftpgo/templates/"
  ${BASE_DIR}/static/*: "/usr/share/sftpgo/static/"
  ${BASE_DIR}/static/css/*: "/usr/share/sftpgo/static/css/"
  ${BASE_DIR}/static/js/*: "/usr/share/sftpgo/static/js/"
  ${BASE_DIR}/static/vendor/bootstrap/js/*: "/usr/share/sftpgo/static/vendor/bootstrap/js/"
  ${BASE_DIR}/static/vendor/datatables/*: "/usr/share/sftpgo/static/vendor/datatables/"
  ${BASE_DIR}/static/vendor/fontawesome-free/css/*: "/usr/share/sftpgo/static/vendor/fontawesome-free/css/"
  ${BASE_DIR}/static/vendor/fontawesome-free/svgs/solid/*: "/usr/share/sftpgo/static/vendor/fontawesome-free/svgs/solid/"
  ${BASE_DIR}/static/vendor/fontawesome-free/webfonts/*: "/usr/share/sftpgo/static/vendor/fontawesome-free/webfonts/"
  ${BASE_DIR}/static/vendor/jquery/*: "/usr/share/sftpgo/static/vendor/jquery/"
  ${BASE_DIR}/static/vendor/jquery-easing/*: "/usr/share/sftpgo/static/vendor/jquery-easing/"
  ${BASE_DIR}/static/vendor/moment/js/*: "/usr/share/sftpgo/static/vendor/moment/js/"
  ${BASE_DIR}/static/vendor/tempusdominus/css/*: "/usr/share/sftpgo/static/vendor/tempusdominus/css/"
  ${BASE_DIR}/static/vendor/tempusdominus/js/*: "/usr/share/sftpgo/static/vendor/tempusdominus/js/"

config_files:
  ./sftpgo.json: "/etc/sftpgo/sftpgo.json"

empty_folders:
  - /var/lib/sftpgo

overrides:
  deb:
    recommends:
      - bash-completion
      - python3-requests
    scripts:
      postinstall: ../scripts/deb/postinstall.sh
      preremove: ../scripts/deb/preremove.sh
      postremove: ../scripts/deb/postremove.sh
  rpm:
    recommends:
      - bash-completion
      # centos 8 has python3-requests, centos 6/7 python-requests
    scripts:
      postinstall: ../scripts/rpm/postinstall
      preremove: ../scripts/rpm/preremove
      postremove: ../scripts/rpm/postremove

rpm:
  compression: lzma

  config_noreplace_files:
    ./sftpgo.json: "/etc/sftpgo/sftpgo.json"

EOF

NFPM_VERSION=1.8.0

curl --retry 5 --retry-delay 2 --connect-timeout 10 -L -O \
  https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/nfpm_${NFPM_VERSION}_Linux_x86_64.tar.gz
tar xvf nfpm_1.8.0_Linux_x86_64.tar.gz nfpm
chmod 755 nfpm
mkdir deb
./nfpm -f nfpm.yaml pkg -p deb -t deb
mkdir rpm
./nfpm -f nfpm.yaml pkg -p rpm -t rpm