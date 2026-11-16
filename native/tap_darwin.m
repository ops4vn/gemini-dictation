#import <CoreGraphics/CoreGraphics.h>
#import <ApplicationServices/ApplicationServices.h>
#import <AppKit/AppKit.h>
#import <CoreFoundation/CoreFoundation.h>
#include <pthread.h>

// CGEventTap listening for Option key flagsChanged (keycodes 58=left, 61=right).
// Callback forwards (eventType, hasOptionFlag) to the Go side.

static CFMachPortRef g_tap = NULL;
static CFRunLoopSourceRef g_tapSrc = NULL;
static CFRunLoopRef g_tapLoop = NULL;
static id g_napActivity = nil;
static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
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

// Dedicated runloop thread: on the main runloop the tap starves whenever the
// webview stalls main (wails dispatch storms) and macOS disables it for good.
static void *tapThread(void *unused) {
    pthread_mutex_lock(&g_mu);
    g_tapLoop = CFRunLoopGetCurrent();
    CFRetain(g_tapLoop);
    CFRunLoopAddSource(g_tapLoop, g_tapSrc, kCFRunLoopDefaultMode);
    pthread_mutex_unlock(&g_mu);
    CFRunLoopRun();
    return NULL;
}

int DictationTapStart(void (*cb)(int, int)) {
    if (g_tap) return 1; // already running
    g_callback = cb;
    // Belt-and-suspenders with NSAppSleepDisabled in Info.plist: a napped
    // process freezes the tap runloop and macOS disables the tap for good.
    if (!g_napActivity) {
        g_napActivity = [[NSProcessInfo processInfo]
            beginActivityWithOptions:NSActivityUserInitiatedAllowingIdleSystemSleep
                               reason:@"dictation key listener must stay live"];
    }
    CGEventMask mask = CGEventMaskBit(kCGEventFlagsChanged);
    g_tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                             kCGEventTapOptionListenOnly, mask, tapCB, NULL);
    if (!g_tap) return 0;
    g_tapSrc = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, g_tap, 0);
    if (!g_tapSrc) {
        CFRelease(g_tap); g_tap = NULL;
        return 0;
    }
    pthread_t t;
    pthread_create(&t, NULL, tapThread, NULL);
    pthread_detach(t);
    CGEventTapEnable(g_tap, true);
    return 1;
}

void DictationTapStop(void) {
    if (g_tap) {
        CGEventTapEnable(g_tap, false);
        if (g_tapLoop) {
            CFRunLoopStop(g_tapLoop);
            CFRelease(g_tapLoop);
            g_tapLoop = NULL;
        }
        if (g_tapSrc) {
            CFRelease(g_tapSrc);
            g_tapSrc = NULL;
        }
        CFRelease(g_tap);
        g_tap = NULL;
        g_callback = NULL;
    }
}

// TRUE Input Monitoring state — CGEventTapIsEnabled is a false positive on
// fresh machines (the tap object exists, macOS just never delivers events).
int DictationTapPermission(void) {
    return CGPreflightListenEventAccess() ? 1 : 0;
}

// Ask macOS to prompt for Input Monitoring (the one prompt API that exists).
int DictationTapRequest(void) {
    return CGRequestListenEventAccess() ? 1 : 0;
}

// Ask macOS to show the Accessibility trust dialog (with a shortcut into
// System Settings). Returns current trust state.
int DictationPromptAccessibility(void) {
    NSDictionary *opts = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt: @YES};
    return AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)opts) ? 1 : 0;
}

int DictationTapIsEnabled(void) {
    return (g_tap && CGEventTapIsEnabled(g_tap)) ? 1 : 0;
}

// Force re-enable from the Go watchdog — the in-callback re-enable never
// runs when the disable notice itself was lost (observed live).
void DictationTapReenable(void) {
    if (g_tap) CGEventTapEnable(g_tap, true);
}

// Full recreate when re-enable doesn't stick (macOS re-disables a tap whose
// eligibility lapsed). The tap is rebuilt ON THE TAP THREAD via
// CFRunLoopPerformBlock — never tear the thread down (Stop+Start inline raced
// the dying thread and SIGSEGV'd at pthread_create; crash 15:51:47).
int DictationTapRestart(void) {
    pthread_mutex_lock(&g_mu);
    int scheduled = (g_callback && g_tapLoop) ? 1 : 0;
    if (scheduled) {
        CFRunLoopRef loop = g_tapLoop;
        CFRunLoopPerformBlock(loop, kCFRunLoopDefaultMode, ^{
            if (g_tap) {
                CGEventTapEnable(g_tap, false);
                if (g_tapSrc) {
                    CFRunLoopRemoveSource(CFRunLoopGetCurrent(), g_tapSrc, kCFRunLoopDefaultMode);
                    CFRelease(g_tapSrc);
                    g_tapSrc = NULL;
                }
                CFRelease(g_tap);
                g_tap = NULL;
            }
            CGEventMask mask = CGEventMaskBit(kCGEventFlagsChanged);
            g_tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                                     kCGEventTapOptionListenOnly, mask, tapCB, NULL);
            if (g_tap) {
                g_tapSrc = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, g_tap, 0);
                if (g_tapSrc) {
                    CFRunLoopAddSource(CFRunLoopGetCurrent(), g_tapSrc, kCFRunLoopDefaultMode);
                    CGEventTapEnable(g_tap, true);
                }
            }
        });
        CFRunLoopWakeUp(loop);
    }
    pthread_mutex_unlock(&g_mu);
    return scheduled;
}

// cursor position (top-left global coords)
int DictationCursorPos(double *x, double *y) {    CGEventRef ev = CGEventCreate(NULL);
    if (!ev) return 0;
    CGPoint p = CGEventGetLocation(ev);
    CFRelease(ev);
    *x = p.x; *y = p.y;
    return 1;
}
