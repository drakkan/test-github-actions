#!/bin/sh
set -e

if [ "$1" = "configure" ]; then
  # Add user and group
  if ! getent group sftpgo >/dev/null; then
    groupadd --system sftpgo
  fi
  if ! getent passwd sftpgo >/dev/null; then
    useradd --system \
      --gid sftpgo \
      --no-create-home \
      --home-dir /var/lib/sftpgo \
      --shell /usr/sbin/nologin \
      --comment "SFTPGo user" \
      sftpgo
  fi

  if [ -z "$2" ]; then
    # initialize data provider
    /usr/bin/sftpgo initprovider -c /etc/sftpgo
    # ensure files and folders have the appropriate permissions
    chown -R sftpgo:sftpgo /etc/sftpgo /var/lib/sftpgo
    chmod 750 /etc/sftpgo /var/lib/sftpgo
    chmod 640 /etc/sftpgo/sftpgo.json /etc/sftpgo/sftpgo.env
	echo "Please be sure to have the python3-requests package installed if you want to use the REST API CLI"
  fi
fi

if [ "$1" = "configure" ] || [ "$1" = "abort-upgrade" ] || [ "$1" = "abort-deconfigure" ] || [ "$1" = "abort-remove" ] ; then
  # This will only remove masks created by d-s-h on package removal.
  deb-systemd-helper unmask sftpgo.service >/dev/null || true

  # was-enabled defaults to true, so new installations run enable.
  if deb-systemd-helper --quiet was-enabled sftpgo.service; then
    # Enables the unit on first installation, creates new
    # symlinks on upgrades if the unit file has changed.
    deb-systemd-helper enable sftpgo.service >/dev/null || true
    deb-systemd-invoke start sftpgo.service >/dev/null || true
  else
    # Update the statefile to add new symlinks (if any), which need to be
    # cleaned up on purge. Also remove old symlinks.
    deb-systemd-helper update-state sftpgo.service >/dev/null || true
  fi

  # Restart only if it was already started
  if [ -d /run/systemd/system ]; then
    systemctl --system daemon-reload >/dev/null || true
    if [ -n "$2" ]; then
      deb-systemd-invoke try-restart sftpgo.service >/dev/null || true
    fi
  fi
fi
