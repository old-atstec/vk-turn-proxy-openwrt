# vk-turn-proxy: the router daemon (cmd/vk-turn-proxyd), the netifd protocol
# handler "vkturn" and the rpcd object "vkturn".
#
# Built from this repository's source at a pinned commit, by the SDK's Go
# (packages feed, lang/golang). openwrt/build.sh builds it from a local tree
# instead (USE_SOURCE_DIR).

include $(TOPDIR)/rules.mk

PKG_NAME:=vk-turn-proxy
PKG_VERSION:=0.1.0
PKG_RELEASE:=8

# VKTURN_SRC (openwrt/build.sh): a local tree instead of the pinned commit,
# VKTURN_COMMIT its commit for the version string. Variables of our own: a
# USE_SOURCE_DIR on make's command line would reach every package built.
ifneq ($(VKTURN_SRC),)
  USE_SOURCE_DIR:=$(VKTURN_SRC)
  PKG_SOURCE_VERSION:=$(VKTURN_COMMIT)
else
  PKG_SOURCE_PROTO:=git
  PKG_SOURCE_URL:=https://github.com/old-atstec/vk-turn-proxy-openwrt.git
  PKG_SOURCE_VERSION:=COMMIT_PLACEHOLDER
  PKG_MIRROR_HASH:=skip
endif

PKG_LICENSE:=GPL-3.0-only
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=old-atstec

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/cacggghp/vk-turn-proxy
GO_PKG_BUILD_PKG:=$(GO_PKG)/cmd/vk-turn-proxyd
GO_PKG_LDFLAGS_X:= \
	main.version=$(PKG_VERSION)-$(PKG_RELEASE) \
	main.commit=$(shell echo '$(PKG_SOURCE_VERSION)' | cut -c1-12)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/vk-turn-proxy
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=WireGuard over the TURN relays of VK calls (proto vkturn)
  URL:=https://github.com/old-atstec/vk-turn-proxy-openwrt
  DEPENDS:=$(GO_ARCH_DEPENDS) +kmod-tun +ca-bundle +rpcd +rpcd-mod-ucode
endef

define Package/vk-turn-proxy/description
  A netifd protocol "vkturn": WireGuard (wireguard-go) over DTLS-SRTP over
  the TURN relays of VK calls to a vk-turn-proxy server (-srtp). The tunnel
  is an ordinary interface — up only while it carries traffic — that
  NetShift or the routing table can send traffic into. The daemon never
  changes the default route, the DNS or any route of the router.
endef

define Package/vk-turn-proxy/install
	$(call GoPackage/Package/Install/Bin,$(PKG_INSTALL_DIR))
	$(INSTALL_DIR) $(1)/usr/sbin
	$(INSTALL_BIN) $(PKG_INSTALL_DIR)/usr/bin/vk-turn-proxyd $(1)/usr/sbin/
	$(INSTALL_DIR) $(1)/lib/netifd/proto
	$(INSTALL_BIN) ./files/vkturn.sh $(1)/lib/netifd/proto/
	$(INSTALL_BIN) ./files/vkturn-notify.sh $(1)/lib/netifd/
	$(INSTALL_DIR) $(1)/usr/share/rpcd/ucode
	$(INSTALL_DATA) ./files/vkturn.rpcd.uc $(1)/usr/share/rpcd/ucode/vkturn
endef

define Package/vk-turn-proxy/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
/etc/init.d/rpcd reload
# An upgrade: the running daemons still run the old binary — restart their
# interfaces (each gives its TURN allocations back first).
if [ "$${PKG_UPGRADE}" = 1 ]; then
	for dir in /var/run/vkturn/*/; do
		[ -f "$${dir}config" ] || continue
		ifup "$$(basename "$$dir")"
	done
fi
exit 0
endef

define Package/vk-turn-proxy/prerm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
# The running interfaces stop with their daemon: netifd tears them down when
# the handler is gone at the next reload; stop them now so that the TURN
# allocations are given back while the daemon still exists.
for dir in /var/run/vkturn/*/; do
	[ -d "$$dir" ] || continue
	ifdown "$$(basename "$$dir")" 2>/dev/null
done
exit 0
endef

define Package/vk-turn-proxy/postrm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
# The run directory and the credential cache (a cache: VK mints anew).
rm -rf /var/run/vkturn /etc/vkturn
/etc/init.d/rpcd reload
exit 0
endef

$(eval $(call GoBinPackage,vk-turn-proxy))
$(eval $(call BuildPackage,vk-turn-proxy))
