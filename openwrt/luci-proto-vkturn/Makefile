# luci-proto-vkturn: the LuCI protocol form of proto vkturn
# (Network → Interfaces) and the status page (Status → VK TURN).

include $(TOPDIR)/rules.mk

LUCI_TITLE:=Support for VK TURN interfaces (vk-turn-proxy)
LUCI_DEPENDS:=+vk-turn-proxy +luci-base
LUCI_PKGARCH:=all

PKG_VERSION:=0.1.0
PKG_RELEASE:=5
PKG_LICENSE:=GPL-3.0-only
PKG_MAINTAINER:=old-atstec

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
