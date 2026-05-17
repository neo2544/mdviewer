// Apple Event handler for kAEOpenDocuments.
// Triggered when the user double-clicks a registered file type, drags a
// file onto the app icon, or runs `open -a MdViewer foo.md`.
//
// This file is compiled by cgo on darwin only and links into the same
// binary as menubar_darwin.go, which provides the goEnqueueOpenFile()
// callback.

#import <Cocoa/Cocoa.h>

extern void goEnqueueOpenFile(char *path);

@interface MdViewerOpenHandler : NSObject
- (void)handleOpenDocs:(NSAppleEventDescriptor *)event
        withReplyEvent:(NSAppleEventDescriptor *)reply;
@end

@implementation MdViewerOpenHandler

- (void)handleOpenDocs:(NSAppleEventDescriptor *)event
        withReplyEvent:(NSAppleEventDescriptor *)reply {
    (void)reply;
    NSAppleEventDescriptor *list = [event paramDescriptorForKeyword:keyDirectObject];
    if (!list) return;
    NSInteger count = [list numberOfItems];
    for (NSInteger i = 1; i <= count; i++) {
        NSAppleEventDescriptor *desc = [list descriptorAtIndex:i];
        NSURL *url = nil;

        // Newer macOS sends typeFileURL descriptors; older ones send typeAlias.
        NSAppleEventDescriptor *coerced = [desc coerceToDescriptorType:typeFileURL];
        if (coerced) {
            NSString *str = [[NSString alloc] initWithData:[coerced data]
                                                  encoding:NSUTF8StringEncoding];
            if (str) url = [NSURL URLWithString:str];
        }
        if (!url) {
            NSString *path = [desc stringValue];
            if (path) url = [NSURL fileURLWithPath:path];
        }
        if (!url) continue;

        const char *p = [[url path] UTF8String];
        if (p) goEnqueueOpenFile((char *)p);
    }
}

@end

static MdViewerOpenHandler *gOpenHandler = nil;

void RegisterMdViewerOpenHandler(void) {
    if (!gOpenHandler) {
        gOpenHandler = [[MdViewerOpenHandler alloc] init];
    }
    [[NSAppleEventManager sharedAppleEventManager]
        setEventHandler:gOpenHandler
        andSelector:@selector(handleOpenDocs:withReplyEvent:)
        forEventClass:kCoreEventClass
        andEventID:kAEOpenDocuments];
}
