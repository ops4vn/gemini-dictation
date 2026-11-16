#import <CoreGraphics/CoreGraphics.h>
#import <AppKit/AppKit.h>
#import <CoreFoundation/CoreFoundation.h>

// CGEventTap listening for Option key flagsChanged (keycodes 58=left, 61=right).
// Callback forwards (eventType, hasOptionFlag) to the Go side.

static CFMachPortRef g_tap = NULL;
static CFRunLoopSourceRef g_tapSrc = NULL;
static void (*g_callback)(int eventType, int hasOption) = NULL;

static CGEventRef tapCB(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *refcon) {
    // macOS disables a tap whose callback responds too slowly (main-runloop
    // stalls from the webview) — re-enable or the listener dies for good.
    if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
        CGEventTapEnable(g_tap, true);
        return NULL;
    }
    if (g_callback) {
        int hasOpt = 0;
        if (type == kCGEventFlagsChanged) {
            CGEventFlags f = CGEventGetFlags(event);
            hasOpt = (f & kCGEventFlagMaskAlternate) ? 1 : 0;
        }
        g_callback((int)type, hasOpt);
    }
    return event;
}

int DictationTapStart(void (*cb)(int, int)) {
    if (g_tap) return 1; // already running
    g_callback = cb;
    CGEventMask mask = CGEventMaskBit(kCGEventFlagsChanged);
    g_tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                             kCGEventTapOptionListenOnly, mask, tapCB, NULL);
    if (!g_tap) return 0;
    g_tapSrc = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, g_tap, 0);
    if (!g_tapSrc) {
        CFRelease(g_tap); g_tap = NULL;
        return 0;
    }
    CFRunLoopAddSource(CFRunLoopGetMain(), g_tapSrc, kCFRunLoopCommonModes);
    CGEventTapEnable(g_tap, true);
    return 1;
}

void DictationTapStop(void) {
    if (g_tap) {
        CGEventTapEnable(g_tap, false);
        if (g_tapSrc) {
            CFRunLoopRemoveSource(CFRunLoopGetMain(), g_tapSrc, kCFRunLoopCommonModes);
            CFRelease(g_tapSrc);
            g_tapSrc = NULL;
        }
        CFRelease(g_tap);
        g_tap = NULL;
        g_callback = NULL;
    }
}

// cursor position (top-left global coords)
int DictationCursorPos(double *x, double *y) {
    CGEventRef ev = CGEventCreate(NULL);
    if (!ev) return 0;
    CGPoint p = CGEventGetLocation(ev);
    CFRelease(ev);
    *x = p.x; *y = p.y;
    return 1;
}
