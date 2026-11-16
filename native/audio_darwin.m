#include <pthread.h>
#import <AVFoundation/AVFoundation.h>
#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>

// WKWebView repaints an opaque white page background unless told otherwise —
// hit every descendant webview (wails nests them).
static void makeWebViewTransparent(NSView *view) {
    if ([view isKindOfClass:[WKWebView class]]) {
        WKWebView *wk = (WKWebView *)view;
        wk.underPageBackgroundColor = [NSColor clearColor];
        [wk setValue:@(NO) forKey:@"drawsBackground"];
        return;
    }
    for (NSView *sub in view.subviews) {
        makeWebViewTransparent(sub);
    }
}

// Restyle the wails window as a true overlay instead of an app window:
// floating status level, joins every space, stays out of Mission Control
// cycles, never hides on deactivate, click-through — and the app itself
// goes Accessory (no Dock icon, no ⌘-Tab) so showing the pill never
// "activates an application". Quit stays available via the dashboard.
int DictationMakeOverlay(const char *title) {
    NSString *t = [NSString stringWithUTF8String:title];
    __block int found = 0;
    dispatch_sync(dispatch_get_main_queue(), ^{
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        for (NSWindow *w in [[NSApplication sharedApplication] windows]) {
            if ([w.title isEqualToString:t]) {
                [w setLevel:NSStatusWindowLevel];
                [w setHidesOnDeactivate:NO];
                [w setCollectionBehavior:
                    NSWindowCollectionBehaviorCanJoinAllSpaces
                  | NSWindowCollectionBehaviorFullScreenAuxiliary
                  | NSWindowCollectionBehaviorIgnoresCycle
                  | NSWindowCollectionBehaviorTransient];
                [w setIgnoresMouseEvents:YES];
                [w setOpaque:NO];
                [w setBackgroundColor:[NSColor clearColor]];
                // the webview can be nested several levels deep — walk all
                // descendants or the pill sits on an opaque white rectangle
                makeWebViewTransparent(w.contentView);
                found = 1;
            }
        }
    });
    return found;
}

// Show the pill WITHOUT activating the app or stealing keyboard focus from
// the user's text field (wails' runtime.WindowShow makes the window key, so
// the paste went into the pill instead of the target app).
int DictationShowWindow(const char *title) {
    NSString *t = [NSString stringWithUTF8String:title];
    __block int found = 0;
    dispatch_sync(dispatch_get_main_queue(), ^{
        for (NSWindow *w in [[NSApplication sharedApplication] windows]) {
            if ([w.title isEqualToString:t]) {
                [w orderFrontRegardless];
                found = 1;
            }
        }
    });
    return found;
}

// Click-through for the pill window: match by title, set ignoresMouseEvents.
// AppKit must be touched on the main thread — hop via dispatch_sync.
int DictationSetClickThrough(const char *title, int enable) {
    NSString *t = [NSString stringWithUTF8String:title];
    __block int found = 0;
    dispatch_sync(dispatch_get_main_queue(), ^{
        for (NSWindow *w in [[NSApplication sharedApplication] windows]) {
            if ([w.title isEqualToString:t]) {
                [w setIgnoresMouseEvents:enable ? YES : NO];
                found = 1;
            }
        }
    });
    return found;
}

// AVAudioRecorder-based lazy mic (same proven path as the Python prototype):
// 16 kHz mono 16-bit PCM into a fixed temp WAV, with metering for the waveform.
// Module-level singleton — one recording session at a time.

static AVAudioRecorder *g_rec = nil;
static NSString *g_path = nil;
static CFRunLoopRef g_audioLoop = NULL;

// Recorder lifecycle must live on a thread with its own runloop: created on
// a bare goroutine thread capture intermittently died (file = zeros, no
// orange mic indicator); via dispatch_sync(main) it died always. A dedicated
// thread mirrors the proven CLI conditions.
static void *audioThread(void *unused) {
    g_audioLoop = CFRunLoopGetCurrent();
    CFRetain(g_audioLoop);
    CFRunLoopSourceContext ctx = {0};
    CFRunLoopSourceRef src = CFRunLoopSourceCreate(kCFAllocatorDefault, 0, &ctx);
    CFRunLoopAddSource(g_audioLoop, src, kCFRunLoopDefaultMode);
    CFRunLoopRun();
    return NULL;
}

static void audioSpawn(void) {
    pthread_t t;
    pthread_create(&t, NULL, audioThread, NULL);
    pthread_detach(t);
}

static CFRunLoopRef audioLoop(void) {
    static pthread_once_t once = PTHREAD_ONCE_INIT;
    pthread_once(&once, audioSpawn);
    // wait for the thread to publish its loop
    for (int i = 0; i < 100 && !g_audioLoop; i++) usleep(1000);
    return g_audioLoop;
}

int DictationAudioStart(void) {
    if (g_rec) return 1; // already armed
    if (!g_path) g_path = @"/tmp/gemini_dictation_segment.wav";
    NSDictionary *settings = @{
        AVFormatIDKey: @(kAudioFormatLinearPCM),
        AVSampleRateKey: @16000.0,
        AVNumberOfChannelsKey: @1,
        AVLinearPCMBitDepthKey: @16,
        AVLinearPCMIsFloatKey: @NO,
        AVLinearPCMIsBigEndianKey: @NO,
        AVLinearPCMIsNonInterleaved: @NO,
    };
    NSURL *url = [NSURL fileURLWithPath:g_path];
    // stale segment = old bytes bleed into the next transcription
    [[NSFileManager defaultManager] removeItemAtURL:url error:nil];
    __block int ok = 0;
    NSDictionary *cfg = settings;
    CFRunLoopRef loop = audioLoop();
    if (!loop) return 0;
    CFRunLoopPerformBlock(loop, kCFRunLoopDefaultMode, ^{
        NSError *err = nil;
        AVAudioRecorder *rec = [[AVAudioRecorder alloc] initWithURL:url settings:cfg error:&err];
        if (!rec) {
            NSLog(@"[gd] mic init error: %@", err);
            return;
        }
        rec.meteringEnabled = YES;
        if (![rec record]) {
            NSLog(@"[gd] mic record refused");
            return;
        }
        g_rec = rec;
        ok = 1;
    });
    CFRunLoopWakeUp(loop);
    for (int i = 0; i < 100 && !ok; i++) usleep(1000); // brief wait for start
    return ok;
}

double DictationAudioDuration(void) {
    return g_rec ? [g_rec currentTime] : 0.0;
}

double DictationAudioLevel(void) {
    // metering is permanently dead in this process (averagePower = -120 on
    // any thread, even main — recorder created via dispatch_sync from cgo).
    // Live level is computed Go-side from the segment file tail instead.
    (void)0;
    return 0.0;
}

void DictationAudioStop(void) {
    if (g_rec && g_audioLoop) {
        CFRunLoopPerformBlock(g_audioLoop, kCFRunLoopDefaultMode, ^{
            [g_rec stop];
        });
        CFRunLoopWakeUp(g_audioLoop);
        return;
    }
    if (g_rec) {
        [g_rec stop];
        g_rec = nil;
    }
}
