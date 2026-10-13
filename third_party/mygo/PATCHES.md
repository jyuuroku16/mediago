# MyGo v0.4.8 macOS 27 status-item compatibility

Production sources copied from github.com/egoist/mygo v0.4.8,
upstream commit c0e1068027faf8b14777c9cb925c937ef34ac2bb. The upstream LICENSE is retained.
The main application keeps v0.4.8 in go.mod and uses this local module via replace.

Only modules.go and internal/darwin/menu.go differ from the upstream production sources.

macOS 27 renders menu-bar controls outside the owning application's window hierarchy.
An inactive NSStatusBarButton can have a proxy window at (0, -33). Opening an
NSPopover from that proxy puts it at the bottom left instead of beneath the icon.
Apple requires custom expanded interfaces to use NSStatusItemExpandedInterfaceDelegate:
https://developer.apple.com/documentation/appkit/nsstatusitemexpandedinterfacedelegate

The compatibility patch:

- registers the macOS 27 expanded-interface delegate, opens via didBegin and
  reports didEnd so the application closes its transient panel;
- cancels the session when the application dismisses its panel;
- retains target/action handling on older macOS versions and right-click handling;
- adds a stable AutosaveName and accessibility label to the status item;
- exposes the native status-item handle for verification.

Remove the replace and these local sources when an upstream release includes the
expanded-interface lifecycle. Do not edit the user's Go module cache.
