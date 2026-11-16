#import <AVFoundation/AVFoundation.h>
#import <AppKit/AppKit.h>

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
    NSError *err = nil;
    AVAudioRecorder *rec = [[AVAudioRecorder alloc] initWithURL:url settings:settings error:&err];
    if (!rec) {
        NSLog(@"mic init error: %@", err);
        return 0;
    }
    rec.meteringEnabled = YES;
    if (![rec record]) {
        NSLog(@"mic record() refused");
        return 0;
    }
    g_rec = rec;
    return 1;
}

double DictationAudioDuration(void) {
    return g_rec ? [g_rec currentTime] : 0.0;
}

double DictationAudioLevel(void) {
    // normalized 0..1 from dBFS (-60..0)
    if (!g_rec) return 0.0;
    [g_rec updateMeters];
    double db = [g_rec averagePowerForChannel:0];
    if (db < -60.0) return 0.0;
    if (db > 0.0) return 1.0;
    return (db + 60.0) / 60.0;
}

void DictationAudioStop(void) {
    if (g_rec) {
        [g_rec stop];
        g_rec = nil;
    }
}
