//go:build desktop && darwin
#import <Cocoa/Cocoa.h>
extern void toolboxTrayAction(int action);
@interface ToolboxTrayTarget : NSObject
- (void)showToolbox:(id)sender;
- (void)quitToolbox:(id)sender;
- (void)applicationActivated:(NSNotification *)notification;
@end
@implementation ToolboxTrayTarget
- (void)showToolbox:(id)sender { toolboxTrayAction(1); }
- (void)quitToolbox:(id)sender { toolboxTrayAction(2); }
- (void)applicationActivated:(NSNotification *)notification {
 for (NSWindow *window in [NSApp windows]) {
  if ([window isVisible] && [window canBecomeMainWindow]) return;
 }
 toolboxTrayAction(1);
}
@end
static NSStatusItem *toolboxStatusItem;
static ToolboxTrayTarget *toolboxTrayTarget;
void toolboxRequestQuit(void) {
 dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}
void toolboxTrayStart(void) {
 dispatch_async(dispatch_get_main_queue(), ^{
  if (toolboxStatusItem) return;
  toolboxTrayTarget = [[ToolboxTrayTarget alloc] init];
  [[NSNotificationCenter defaultCenter] addObserver:toolboxTrayTarget selector:@selector(applicationActivated:) name:NSApplicationDidBecomeActiveNotification object:NSApp];
  toolboxStatusItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength] retain];
  if (@available(macOS 11.0, *)) {
   NSImage *image = [NSImage imageWithSystemSymbolName:@"shippingbox" accessibilityDescription:@"Toolbox"];
   [image setTemplate:YES];
   [[toolboxStatusItem button] setImage:image];
  } else { [[toolboxStatusItem button] setTitle:@"TB"]; }
  [[toolboxStatusItem button] setToolTip:@"Toolbox · 本地 MCP 服务"];
  NSMenu *menu = [[NSMenu alloc] initWithTitle:@"Toolbox"];
  NSMenuItem *show = [[NSMenuItem alloc] initWithTitle:@"显示主窗口" action:@selector(showToolbox:) keyEquivalent:@""];
  [show setTarget:toolboxTrayTarget];[menu addItem:show];[show release];
  [menu addItem:[NSMenuItem separatorItem]];
  NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:@"退出 Toolbox" action:@selector(quitToolbox:) keyEquivalent:@""];
  [quit setTarget:toolboxTrayTarget];[menu addItem:quit];[quit release];
  [toolboxStatusItem setMenu:menu];[menu release];
 });
}
void toolboxTrayStop(void) {
 dispatch_async(dispatch_get_main_queue(), ^{
  if (toolboxStatusItem) { [[NSStatusBar systemStatusBar] removeStatusItem:toolboxStatusItem];[toolboxStatusItem release];toolboxStatusItem=nil; }
  [[NSNotificationCenter defaultCenter] removeObserver:toolboxTrayTarget];
  [toolboxTrayTarget release];toolboxTrayTarget=nil;
 });
}
