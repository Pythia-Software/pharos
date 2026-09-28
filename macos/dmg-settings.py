# dmgbuild settings for the Pharos release disk image; see macos/make-release-dmg.sh.
# dmgbuild supplies `defines` from its -D options.
app = defines["app"]

format = "UDZO"
compression_level = 9
files = [(app, "Pharos.app")]
symlinks = {"Applications": "/Applications"}
background = defines["background"]

# Finder keeps the window on screen; y runs from the bottom of the display.
# The size includes a title bar of about 28 points above the 760 x 440 guide.
window_rect = ((160, 360), (760, 468))
show_status_bar = False
show_tab_view = False
show_toolbar = False
show_pathbar = False
show_sidebar = False

default_view = "icon-view"
arrange_by = None
icon_size = 72
text_size = 12
# Icon centers, in points from the top left of the guide. Items that only
# appear with Finder's Show All Files are moved out of view.
icon_locations = {
    "Pharos.app": (87, 180),
    "Applications": (210, 180),
    ".background.tiff": (1100, 100),
    ".DS_Store": (1100, 100),
    ".fseventsd": (1100, 100),
}
