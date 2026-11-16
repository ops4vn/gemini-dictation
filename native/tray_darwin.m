#import <AppKit/AppKit.h>

// Menu-bar (tray) item: dashboard shortcut + quit — the app is Accessory
// (no Dock icon), so this is its always-visible handle.

extern void DictationTrayOpen(void);
extern void DictationTrayQuit(void);

static NSStatusItem *g_item = nil;

@interface GDTray : NSObject
- (void)open:(id)sender;
- (void)quit:(id)sender;
@end

@implementation GDTray
- (void)open:(id)sender { DictationTrayOpen(); }
- (void)quit:(id)sender { DictationTrayQuit(); }
@end

static GDTray *g_tray = nil;

void DictationTraySetup(void) {
    dispatch_sync(dispatch_get_main_queue(), ^{
        if (g_item) return;
        NSStatusBar *bar = [NSStatusBar systemStatusBar];
        g_item = [bar statusItemWithLength:NSVariableStatusItemLength];
        NSImage *img = [NSImage imageWithSystemSymbolName:@"mic.circle.fill"
                                  accessibilityDescription:@"Gemini Dictation"];
        if (img) {
            g_item.button.image = img;
        } else {
            g_item.button.title = @"🎙";
        }
        g_tray = [[GDTray alloc] init];
        GDTray *tray = g_tray;
        NSMenu *menu = [[NSMenu alloc] initWithTitle:@"Gemini Dictation"];
        NSMenuItem *open = [menu addItemWithTitle:@"Mở bảng điều khiển"
                                           action:@selector(open:) keyEquivalent:@""];
        open.target = tray;
        [menu addItem:[NSMenuItem separatorItem]];
        NSMenuItem *quit = [menu addItemWithTitle:@"Thoát Gemini Dictation"
                                            action:@selector(quit:) keyEquivalent:@"q"];
        quit.target = tray;
        g_item.menu = menu;
    });
}
