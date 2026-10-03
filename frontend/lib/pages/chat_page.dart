import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api_client.dart';
import '../attachments/image_source.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/confirm_dialog.dart';
import '../ui/dialog_band.dart';
import '../ui/feedback.dart';
import '../ui/page_header.dart';
import '../ui/styled_dropdown.dart';
import '../ui/top_toast.dart';

/// 对话页：左侧会话列表（新对话/切换/删除），右侧消息流 + 模型选择 + 输入框。
/// 补全走服务端：按所选模型的出站协议透传上游，整段历史落库可回看。
/// 输入区支持图片附件：回形针选图或 Ctrl+V 粘贴截图，随消息内嵌上行。
class ChatPage extends StatefulWidget {
  ChatPage(
      {super.key,
      required this.client,
      this.onOpenSettings,
      this.active = false,
      ImageSource? imageSource})
      : imageSource = imageSource ?? SystemImageSource();

  final ApiClient client;
  final VoidCallback? onOpenSettings;

  /// 是否正处于前台(由主壳按当前导航传入)。页在 IndexedStack 里常驻,
  /// 别处新建的模型/会话不会触发本页重建,重新激活时静默刷新一次。
  final bool active;

  /// 取图途径：生产用系统实现，widget 测试注入 fake。
  final ImageSource imageSource;

  @override
  State<ChatPage> createState() => _ChatPageState();
}

class _ChatPageState extends State<ChatPage> {
  final TextEditingController _input = TextEditingController();
  final FocusNode _inputFocus = FocusNode();
  final ScrollController _scroll = ScrollController();

  List<ChatSession> _sessions = [];
  List<ChatMessage> _messages = [];
  List<UpstreamModel> _models = [];
  List<PendingImage> _pending = [];
  String? _sessionId;
  String? _modelId;
  bool _loadingSessions = true;
  bool _sending = false;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _inputFocus.onKeyEvent = _onInputKey;
    _load();
  }

  @override
  void didUpdateWidget(ChatPage oldWidget) {
    super.didUpdateWidget(oldWidget);
    // 重新激活(false→true)时静默刷新:别处的改动(新建模型/会话等)
    // 不会触发常驻页重建,不刷新会在选择器里看不到新模型。
    if (!oldWidget.active && widget.active) _refreshQuiet();
  }

  /// 激活沿刷新:只重拉会话与模型列表,不翻 _loadingSessions——
  /// 每次切页都闪整页加载态比旧快照更伤体验;当前会话与消息不动。
  Future<void> _refreshQuiet() async {
    try {
      final sessions = await widget.client.listChatSessions();
      final models = await widget.client.listModels();
      if (!mounted) return;
      setState(() {
        _sessions = sessions;
        _models = models.where((m) => m.enabled).toList();
      });
      _syncModelChoice();
    } catch (_) {
      // 静默刷新失败不打扰:保留旧快照,显式重试走 _load 的错误路径。
    }
  }

  @override
  void dispose() {
    _input.dispose();
    _inputFocus.dispose();
    _scroll.dispose();
    super.dispose();
  }

  /// Enter 发送、Shift+Enter 换行：与常见聊天客户端一致。
  /// Ctrl+V 先问剪贴板有没有图：有图收为附件，无图回落文本粘贴。
  KeyEventResult _onInputKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent) return KeyEventResult.ignored;
    if (event.logicalKey == LogicalKeyboardKey.keyV) {
      final pressed = HardwareKeyboard.instance.logicalKeysPressed;
      final ctrl = pressed.contains(LogicalKeyboardKey.controlLeft) ||
          pressed.contains(LogicalKeyboardKey.controlRight);
      if (ctrl) {
        _pasteClipboard();
        return KeyEventResult.handled;
      }
      return KeyEventResult.ignored;
    }
    if (event.logicalKey != LogicalKeyboardKey.enter &&
        event.logicalKey != LogicalKeyboardKey.numpadEnter) {
      return KeyEventResult.ignored;
    }
    final pressed = HardwareKeyboard.instance.logicalKeysPressed;
    final shifted = pressed.contains(LogicalKeyboardKey.shiftLeft) ||
        pressed.contains(LogicalKeyboardKey.shiftRight);
    if (shifted) return KeyEventResult.ignored;
    _send();
    return KeyEventResult.handled;
  }

  /// 粘贴：读图是异步的，先吞掉事件；剪贴板无图时手动补文本粘贴。
  Future<void> _pasteClipboard() async {
    try {
      final img = await widget.imageSource.readClipboard();
      if (img != null) {
        if (mounted) _addPending([img]);
        return;
      }
    } catch (_) {
      // 读图插件失败不阻断文本粘贴。
    }
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    final text = data?.text;
    if (!mounted || text == null || text.isEmpty) return;
    final sel = _input.selection;
    final start = sel.isValid && sel.start >= 0 ? sel.start : _input.text.length;
    final end = sel.isValid && sel.end >= 0 ? sel.end : _input.text.length;
    _input.value = TextEditingValue(
      text: _input.text.replaceRange(start, end, text),
      selection: TextSelection.collapsed(offset: start + text.length),
    );
  }

  Future<void> _pickImages() async {
    try {
      final imgs = await widget.imageSource.pickFiles();
      if (mounted) _addPending(imgs);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  /// 前端先拦一道超限（与服务端 validateImages 同规则），
  /// 避免把注定失败的请求发出去。
  void _addPending(List<PendingImage> imgs) {
    if (imgs.isEmpty) return;
    final room = maxPendingImages - _pending.length;
    final accepted = <PendingImage>[];
    var rejected = 0;
    for (final img in imgs) {
      if (img.bytes.isEmpty ||
          img.bytes.length > maxImageBytes ||
          accepted.length >= room) {
        rejected++;
        continue;
      }
      accepted.add(img);
    }
    if (accepted.isNotEmpty) {
      setState(() => _pending = [..._pending, ...accepted]);
    }
    if (rejected > 0) {
      showError(context,
          '已忽略 $rejected 张图片：最多 $maxPendingImages 张、单张不超过 4 MB');
    }
  }

  void _removePending(int i) => setState(() => _pending.removeAt(i));

  Future<void> _load() async {
    setState(() {
      _loadingSessions = true;
      _error = null;
    });
    try {
      final sessions = await widget.client.listChatSessions();
      final models = await widget.client.listModels();
      if (!mounted) return;
      setState(() {
        _sessions = sessions;
        _models = models.where((m) => m.enabled).toList();
        _loadingSessions = false;
      });
      if (_sessionId == null && _sessions.isNotEmpty) {
        await _select(_sessions.first.id);
      } else if (_sessionId != null) {
        await _select(_sessionId!);
      }
      _syncModelChoice();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loadingSessions = false;
      });
    }
  }

  /// 模型选择回显：会话最近用过的模型优先，否则第一个可用模型。
  void _syncModelChoice() {
    if (_models.isEmpty) {
      return;
    }
    final ids = _models.map((m) => m.id).toList();
    final sess = _sessions.where((s) => s.id == _sessionId).firstOrNull;
    if (_modelId == null || !ids.contains(_modelId)) {
      _modelId = (sess != null && ids.contains(sess.modelId))
          ? sess.modelId
          : ids.first;
    }
  }

  Future<void> _select(String id) async {
    setState(() => _sessionId = id);
    try {
      final (sess, msgs) = await widget.client.getChatMessages(id);
      if (!mounted) return;
      setState(() {
        _messages = msgs;
        if (sess.modelId.isNotEmpty) _modelId = sess.modelId;
      });
      _syncModelChoice();
      _toEnd();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _newSession() async {
    try {
      final s = await widget.client.createChatSession();
      if (!mounted) return;
      setState(() {
        _sessions = [s, ..._sessions];
        _messages = [];
        _sessionId = s.id;
      });
      _syncModelChoice();
      _inputFocus.requestFocus();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _deleteSession(ChatSession s) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => ConfirmDialog(
        title: '删除会话',
        message: '将删除「${s.title}」及其全部消息，不可恢复。',
      ),
    );
    if (ok != true) return;
    try {
      await widget.client.deleteChatSession(s.id);
      if (!mounted) return;
      setState(() {
        _sessions = _sessions.where((x) => x.id != s.id).toList();
        if (_sessionId == s.id) {
          _sessionId = _sessions.isNotEmpty ? _sessions.first.id : null;
          _messages = [];
        }
      });
      if (_sessionId != null) await _select(_sessionId!);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _clearMessages() async {
    final id = _sessionId;
    if (id == null) return;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => const ConfirmDialog(
        title: '清除消息',
        message: '将清除当前会话的全部消息，会话本身保留。',
        confirmLabel: '清除',
        severity: ConfirmSeverity.warning,
      ),
    );
    if (ok != true) return;
    try {
      await widget.client.clearChatMessages(id);
      if (!mounted) return;
      setState(() => _messages = []);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  /// 重命名会话(KiroaaS 行内铅笔)：弹窗预填现标题，提交后原地替换列表项。
  Future<void> _renameSession(ChatSession s) async {
    final title = await showDialog<String>(
      context: context,
      builder: (ctx) => _RenameDialog(initial: s.title),
    );
    if (title == null || title.isEmpty || title == s.title) return;
    try {
      final updated = await widget.client.renameChatSession(s.id, title);
      if (!mounted) return;
      setState(() {
        _sessions = [
          for (final x in _sessions)
            if (x.id == updated.id) updated else x
        ];
      });
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _send() async {
    final text = _input.text.trim();
    // 纯图可发：文本与附件不可同空（与服务端 validateImages 同规则）。
    if ((text.isEmpty && _pending.isEmpty) || _sending) return;
    final model = _modelId;
    if (model == null) {
      showError(context, '没有可用模型：先到模型页启用一个模型');
      return;
    }
    var id = _sessionId;
    if (id == null) {
      try {
        final s = await widget.client.createChatSession();
        if (!mounted) return;
        setState(() {
          _sessions = [s, ..._sessions];
          _sessionId = s.id;
        });
        id = s.id;
      } catch (e) {
        if (mounted) showError(context, e);
        return;
      }
    }
    final pending = List<PendingImage>.of(_pending);
    setState(() {
      _sending = true;
      _input.clear();
      _pending = [];
    });
    try {
      final msgs = await widget.client.sendChatMessage(
        id,
        modelId: model,
        content: text,
        images: [
          for (final p in pending)
            ChatAttachment(mime: p.mime, data: base64Encode(p.bytes)),
        ],
      );
      if (!mounted) return;
      final sessions = await widget.client.listChatSessions();
      if (!mounted) return;
      setState(() {
        _messages = msgs;
        _sessions = sessions;
        _sending = false;
      });
      _toEnd();
      _inputFocus.requestFocus();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _sending = false;
        _pending = pending;
      });
      showError(context, e);
      // 用户消息已落库：失败也重拉消息与会话，让发出去的话和自动标题
      // 同步上屏，避免界面上「看不到自己发了什么」。
      await _select(id);
      try {
        final sessions = await widget.client.listChatSessions();
        if (mounted) setState(() => _sessions = sessions);
      } catch (_) {
        // 会话列表刷新失败不叠加第二个错误提示。
      }
      // 输入与附件还回去：失败不该让用户重打一遍、重贴一遍。
      _input.text = text;
      _input.selection =
          TextSelection.collapsed(offset: _input.text.length);
      _inputFocus.requestFocus();
    }
  }

  void _toEnd() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_scroll.hasClients) return;
      _scroll.jumpTo(_scroll.position.maxScrollExtent);
    });
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    if (_loadingSessions) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_error != null) {
      return ErrorPanel(
        error: _error!,
        onRetry: _load,
        onOpenSettings: widget.onOpenSettings,
      );
    }
    return Row(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _sessionPane(t),
        VerticalDivider(width: 1, thickness: 1, color: t.border),
        Expanded(child: _chatPane(t)),
      ],
    );
  }

  Widget _sessionPane(AppTokens t) {
    return SizedBox(
      width: 250,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(18, 18, 18, 10),
            child: Row(
              children: [
                Text('对话',
                    style: TextStyle(
                        fontSize: 15, fontWeight: FontWeight.w600, color: t.ink)),
                const Spacer(),
                Text('${_sessions.length} 个',
                    style: TextStyle(fontSize: 12, color: t.faint)),
              ],
            ),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 0, 14, 10),
            child: OutlinedButton.icon(
              onPressed: _newSession,
              icon: const Icon(Icons.add_rounded, size: 15),
              label: const Text('新对话'),
            ),
          ),
          Expanded(
            child: ListView(
              padding: const EdgeInsets.fromLTRB(14, 0, 14, 14),
              children: [
                for (final s in _sessions)
                  _SessionRow(
                    t: t,
                    session: s,
                    selected: s.id == _sessionId,
                    onSelect: () => _select(s.id),
                    onRename: () => _renameSession(s),
                    onDelete: () => _deleteSession(s),
                  ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _chatPane(AppTokens t) {
    final sess = _sessions.where((s) => s.id == _sessionId).firstOrNull;
    if (sess == null) {
      return Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.chat_bubble_outline_rounded, size: 42, color: t.faint),
            const SizedBox(height: 12),
            Text('暂无对话，开一个新会话试试',
                style: TextStyle(fontSize: 13, color: t.faint)),
            const SizedBox(height: 16),
            FilledButton(onPressed: _newSession, child: const Text('新对话')),
          ],
        ),
      );
    }
    return Column(
      children: [
        PageHeader(
          title: sess.title.isEmpty ? '新对话' : sess.title,
          // 清除=清空当前会话消息(KiroaaS 右上「清除」);删除会话移到侧栏行内垃圾桶。
          trailing: [
            OutlinedButton(
              onPressed: _messages.isEmpty ? null : _clearMessages,
              child: const Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(Icons.delete_sweep_outlined, size: 15),
                  SizedBox(width: 6),
                  Text('清除'),
                ],
              ),
            ),
          ],
        ),
        const Divider(height: 1),
        Expanded(
          child: _messages.isEmpty && !_sending
              ? Center(
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Icon(Icons.forum_outlined, size: 40, color: t.faint),
                      const SizedBox(height: 12),
                      Text('向当前模型提问，支持 Ctrl+V 粘贴截图',
                          style: TextStyle(fontSize: 13, color: t.faint)),
                    ],
                  ),
                )
              : ListView(
                  controller: _scroll,
                  padding: const EdgeInsets.fromLTRB(24, 20, 24, 12),
                  children: [
                    for (final m in _messages) _bubble(t, m),
                    if (_sending) _thinking(t),
                  ],
                ),
        ),
        _inputBar(t),
      ],
    );
  }

  Widget _bubble(AppTokens t, ChatMessage m) {
    final isUser = m.role == 'user';
    // 助手回复按 ``` 围块切出代码段；用户气泡只渲染图片+纯文本。
    final body = isUser
        ? [
            if (m.attachments.isNotEmpty) _messageImages(t, m),
            if (m.content.isNotEmpty) _plainText(t, m.content, isUser: true),
          ]
        : [
            for (final seg in _splitSegments(m.content))
              if (seg.lang == null)
                _plainText(t, seg.text, isUser: false)
              else
                _codeBlock(t, seg),
          ];
    final bubble = Container(
      margin: const EdgeInsets.only(bottom: 14),
      constraints: const BoxConstraints(maxWidth: 620),
      padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
      decoration: BoxDecoration(
        color: isUser ? t.primarySoft : t.surface,
        border: Border.all(color: isUser ? Colors.transparent : t.border),
        borderRadius: BorderRadius.only(
          topLeft: const Radius.circular(12),
          topRight: const Radius.circular(12),
          bottomLeft: Radius.circular(isUser ? 12 : 4),
          bottomRight: Radius.circular(isUser ? 4 : 12),
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: body,
      ),
    );
    return Align(
      alignment: isUser ? Alignment.centerRight : Alignment.centerLeft,
      child: bubble,
    );
  }

  Widget _plainText(AppTokens t, String text, {required bool isUser}) {
    return SelectableText(
      text,
      style: TextStyle(
        fontSize: 13.5,
        height: 1.6,
        color: isUser ? t.primaryInk : t.ink,
      ),
    );
  }

  /// 用户消息里的图片：圆角限宽，点击弹原图预览。
  Widget _messageImages(AppTokens t, ChatMessage m) {
    return Padding(
      padding: EdgeInsets.only(bottom: m.content.isEmpty ? 0 : 8),
      child: Wrap(
        spacing: 8,
        runSpacing: 8,
        children: [for (final a in m.attachments) _attachmentView(t, a)],
      ),
    );
  }

  Widget _attachmentView(AppTokens t, ChatAttachment a) {
    final bytes = _decodeImage(a.data);
    if (bytes == null) {
      return Container(
        width: 120,
        height: 60,
        alignment: Alignment.center,
        decoration: BoxDecoration(
          color: t.bg,
          border: Border.all(color: t.border),
          borderRadius: BorderRadius.circular(8),
        ),
        child: Text('图片已损坏', style: TextStyle(fontSize: 11, color: t.faint)),
      );
    }
    return MouseRegion(
      cursor: SystemMouseCursors.click,
      child: GestureDetector(
        onTap: () => _previewImage(bytes),
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 300, maxHeight: 220),
          child: ClipRRect(
            borderRadius: BorderRadius.circular(8),
            child: Image.memory(bytes, fit: BoxFit.contain),
          ),
        ),
      ),
    );
  }

  void _previewImage(Uint8List bytes) {
    showDialog(
      context: context,
      builder: (ctx) => Dialog(
        backgroundColor: Colors.transparent,
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 900, maxHeight: 680),
          child: ClipRRect(
            borderRadius: BorderRadius.circular(12),
            child: Image.memory(bytes, fit: BoxFit.contain),
          ),
        ),
      ),
    );
  }

  /// 代码块：t.bg 底 + 描边 + radiusCtrl 圆角，头行语言标签 + 复制钮。
  Widget _codeBlock(AppTokens t, _Segment seg) {
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(top: 8),
      decoration: BoxDecoration(
        color: t.bg,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Container(
            padding: const EdgeInsets.fromLTRB(12, 4, 6, 4),
            decoration: BoxDecoration(
              border: Border(bottom: BorderSide(color: t.border)),
            ),
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    seg.lang!.isEmpty ? 'code' : seg.lang!,
                    style: TextStyle(fontSize: 11, color: t.faint),
                  ),
                ),
                IconButton(
                  tooltip: '复制',
                  onPressed: () async {
                    await Clipboard.setData(ClipboardData(text: seg.text));
                    if (!mounted) return;
                    TopToast.show(context, '已复制代码');
                  },
                  icon: Icon(Icons.copy_all_rounded, size: 13, color: t.faint),
                  splashRadius: 13,
                  visualDensity: VisualDensity.compact,
                  padding: EdgeInsets.zero,
                  constraints:
                      const BoxConstraints(minWidth: 22, minHeight: 22),
                ),
              ],
            ),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 8, 12, 10),
            child: SelectableText(
              seg.text,
              style: TextStyle(
                fontFamily: AppConst.fontMono,
                fontSize: 12.5,
                height: 1.55,
                color: t.ink,
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _thinking(AppTokens t) {
    return Align(
      alignment: Alignment.centerLeft,
      child: Container(
        margin: const EdgeInsets.only(bottom: 14),
        padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
        decoration: BoxDecoration(
          color: t.surface,
          border: Border.all(color: t.border),
          borderRadius: const BorderRadius.only(
            topLeft: Radius.circular(12),
            topRight: Radius.circular(12),
            bottomLeft: Radius.circular(4),
            bottomRight: Radius.circular(12),
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            SizedBox(
              width: 13,
              height: 13,
              child: CircularProgressIndicator(strokeWidth: 2, color: t.primary),
            ),
            const SizedBox(width: 9),
            Text('思考中…', style: TextStyle(fontSize: 12, color: t.faint)),
          ],
        ),
      ),
    );
  }

  Widget _inputBar(AppTokens t) {
    // 方案A 输入卡：缩略图 strip（有附件时）→ 无边框输入框 →
    // 底部工具行（模型胶囊 + 回形针 + 圆形发送钮同排）。
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 4, 20, 16),
      child: Container(
        decoration: BoxDecoration(
          color: t.surface,
          border: Border.all(color: t.border),
          borderRadius: BorderRadius.circular(14),
        ),
        padding: const EdgeInsets.all(12),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (_pending.isNotEmpty) ...[
              SizedBox(
                height: 62,
                child: ListView(
                  scrollDirection: Axis.horizontal,
                  padding: const EdgeInsets.only(top: 6),
                  children: [
                    for (var i = 0; i < _pending.length; i++)
                      Padding(
                        padding: const EdgeInsets.only(right: 10),
                        child: _PendingThumb(
                          t: t,
                          image: _pending[i],
                          onRemove: () => _removePending(i),
                        ),
                      ),
                  ],
                ),
              ),
              const SizedBox(height: 8),
            ],
            TextField(
              controller: _input,
              focusNode: _inputFocus,
              minLines: 1,
              maxLines: 5,
              textInputAction: TextInputAction.newline,
              decoration: InputDecoration(
                border: InputBorder.none,
                isCollapsed: true,
                // 发送等待(codex 非流式一等就是几十秒)时把提示切到「思考中」,
                // 否则输入框清空后毫无等待反馈,像消息没发出去。
                hintText: _sending ? '正在思考，请稍候…' : '输入消息… Enter 发送，Shift+Enter 换行',
              ),
            ),
            if (_pending.isNotEmpty)
              Padding(
                padding: const EdgeInsets.fromLTRB(4, 8, 0, 0),
                child: Row(
                  children: [
                    Icon(Icons.info_outline_rounded, size: 12, color: t.faint),
                    const SizedBox(width: 4),
                    Text(
                      '图片需模型支持视觉输入，单张不超过 4 MB，最多 $maxPendingImages 张',
                      style: TextStyle(fontSize: 11, color: t.faint),
                    ),
                  ],
                ),
              ),
            const SizedBox(height: 8),
            Row(
              children: [
                SizedBox(
                  width: 200,
                  child: StyledDropdown(
                    value: _modelId,
                    options: _models.map((m) => m.id).toList(),
                    showValue: _modelId != null,
                    dropUp: true,
                    decoration: InputDecoration(
                      isDense: true,
                      filled: true,
                      fillColor: t.bg,
                      contentPadding: const EdgeInsets.symmetric(
                          horizontal: 12, vertical: 8),
                      border: OutlineInputBorder(
                        borderRadius: BorderRadius.circular(16),
                        borderSide: BorderSide.none,
                      ),
                    ),
                    onChanged: (v) => setState(() => _modelId = v),
                  ),
                ),
                const Spacer(),
                IconButton(
                  tooltip: '添加图片（也可 Ctrl+V 粘贴截图）',
                  onPressed: _sending ? null : _pickImages,
                  icon: Icon(Icons.attach_file_rounded, size: 18, color: t.dim),
                  splashRadius: 17,
                  visualDensity: VisualDensity.compact,
                ),
                const SizedBox(width: 4),
                BusyButton(
                  busy: _sending,
                  onPressed: _send,
                  style: FilledButton.styleFrom(
                    minimumSize: const Size(34, 34),
                    padding: EdgeInsets.zero,
                    shape: const CircleBorder(),
                  ),
                  child: const Icon(Icons.send_rounded, size: 16),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

/// 解码内嵌图片；坏数据返回 null，气泡侧渲染占位而非炸页面。
Uint8List? _decodeImage(String data) {
  try {
    final bytes = base64Decode(data);
    return bytes.isEmpty ? null : bytes;
  } catch (_) {
    return null;
  }
}

/// 助手回复的分段结果：lang 为 null 是纯文本段，否则是代码段。
class _Segment {
  const _Segment.text(this.text) : lang = null;
  const _Segment.code(this.lang, this.text);

  final String? lang;
  final String text;
}

/// 简易 ``` 围块分段器：覆盖「读代码回复」的绝大部分收益，
/// 不引 markdown 依赖，样式全走 token。未闭合的围块也按代码段收尾。
List<_Segment> _splitSegments(String content) {
  final re = RegExp(r'```(\w*)[ \t]*\r?\n([\s\S]*?)(?:```|$)');
  final out = <_Segment>[];
  var pos = 0;
  for (final m in re.allMatches(content)) {
    final head = content.substring(pos, m.start);
    if (head.trim().isNotEmpty) out.add(_Segment.text(head.trim()));
    out.add(_Segment.code(m.group(1) ?? '', (m.group(2) ?? '').trimRight()));
    pos = m.end;
  }
  final tail = content.substring(pos);
  if (tail.trim().isNotEmpty) out.add(_Segment.text(tail.trim()));
  if (out.isEmpty) out.add(_Segment.text(content));
  return out;
}

/// 待发图片缩略图：56×56 圆角描边，悬停显现右上移除钮
/// （Opacity+IgnorePointer 手法同 _SessionRow：不占位、不跳高）。
class _PendingThumb extends StatefulWidget {
  const _PendingThumb({
    required this.t,
    required this.image,
    required this.onRemove,
  });

  final AppTokens t;
  final PendingImage image;
  final VoidCallback onRemove;

  @override
  State<_PendingThumb> createState() => _PendingThumbState();
}

class _PendingThumbState extends State<_PendingThumb> {
  bool _hovered = false;

  @override
  Widget build(BuildContext context) {
    final t = widget.t;
    return MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: Stack(
        clipBehavior: Clip.none,
        children: [
          Container(
            width: 56,
            height: 56,
            decoration: BoxDecoration(
              border: Border.all(color: t.border),
              borderRadius: BorderRadius.circular(8),
            ),
            child: ClipRRect(
              borderRadius: BorderRadius.circular(7),
              child: Image.memory(widget.image.bytes, fit: BoxFit.cover),
            ),
          ),
          Positioned(
            top: -6,
            right: -6,
            child: Opacity(
              opacity: _hovered ? 1 : 0,
              child: IgnorePointer(
                ignoring: !_hovered,
                child: GestureDetector(
                  onTap: widget.onRemove,
                  child: Container(
                    width: 17,
                    height: 17,
                    decoration: BoxDecoration(
                      color: t.ink,
                      shape: BoxShape.circle,
                      border: Border.all(color: t.surface, width: 2),
                    ),
                    child: Icon(Icons.close, size: 10, color: t.surface),
                  ),
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// 重命名会话弹窗：控制器归弹窗自身 State 所有，
/// 关闭动画期间字段仍在构建，提前 dispose 会撞「used after being disposed」。
/// 校验通过以修剪后的标题 pop，取消 pop null。
class _RenameDialog extends StatefulWidget {
  const _RenameDialog({required this.initial});

  final String initial;

  @override
  State<_RenameDialog> createState() => _RenameDialogState();
}

class _RenameDialogState extends State<_RenameDialog> {
  late final TextEditingController _controller =
      TextEditingController(text: widget.initial);
  final _formKey = GlobalKey<FormState>();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  void _submit(BuildContext ctx) {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    Navigator.pop(ctx, _controller.text.trim());
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return AlertDialog(
      // 常规级(非破坏):与确认弹窗同构的色带头,只换中性配色。
      clipBehavior: Clip.antiAlias,
      titlePadding: EdgeInsets.zero,
      contentPadding: const EdgeInsets.fromLTRB(22, 16, 22, 4),
      actionsPadding: const EdgeInsets.fromLTRB(22, 14, 22, 17),
      title: DialogBand(
        title: '重命名会话',
        icon: Icons.edit_outlined,
        background: t.bg,
        foreground: t.dim,
      ),
      content: SizedBox(
        width: 420,
        child: Form(
          key: _formKey,
          child: TextFormField(
            controller: _controller,
            autofocus: true,
            decoration: const InputDecoration(
              labelText: '会话标题',
              border: OutlineInputBorder(),
            ),
            validator: (v) =>
                (v == null || v.trim().isEmpty) ? '标题不能为空' : null,
            onFieldSubmitted: (_) => _submit(context),
          ),
        ),
      ),
      actions: [
        OutlinedButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('取消')),
        FilledButton(
          onPressed: () => _submit(context),
          child: const Text('保存'),
        ),
      ],
    );
  }
}

/// 会话行：标题 + 时间，右侧行内重命名/删除(仿 KiroaaS)。
/// 动作悬停显现；选中行常显，保证不悬停也找得到入口。
class _SessionRow extends StatefulWidget {
  const _SessionRow({
    required this.t,
    required this.session,
    required this.selected,
    required this.onSelect,
    required this.onRename,
    required this.onDelete,
  });

  final AppTokens t;
  final ChatSession session;
  final bool selected;
  final VoidCallback onSelect;
  final VoidCallback onRename;
  final VoidCallback onDelete;

  @override
  State<_SessionRow> createState() => _SessionRowState();
}

class _SessionRowState extends State<_SessionRow> {
  bool _hovered = false;

  Widget _action(IconData icon, String tooltip, VoidCallback onPressed) {
    return IconButton(
      tooltip: tooltip,
      icon: Icon(icon, size: 15, color: widget.t.faint),
      splashRadius: 15,
      visualDensity: VisualDensity.compact,
      padding: EdgeInsets.zero,
      constraints: const BoxConstraints(minWidth: 26, minHeight: 26),
      onPressed: onPressed,
    );
  }

  @override
  Widget build(BuildContext context) {
    final t = widget.t;
    final s = widget.session;
    final d = s.updatedAt.toLocal();
    final sub =
        '${d.month}月${d.day}日 ${d.hour.toString().padLeft(2, '0')}:${d.minute.toString().padLeft(2, '0')}';
    final showActions = _hovered || widget.selected;
    return Container(
      margin: const EdgeInsets.only(bottom: 6),
      decoration: BoxDecoration(
        color: widget.selected ? t.primarySoft : Colors.transparent,
        border: Border.all(color: widget.selected ? t.primary : t.border),
        borderRadius: BorderRadius.circular(10),
      ),
      child: MouseRegion(
        onEnter: (_) => setState(() => _hovered = true),
        onExit: (_) => setState(() => _hovered = false),
        child: InkWell(
          borderRadius: BorderRadius.circular(10),
          onTap: widget.onSelect,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 9, 8, 9),
            child: Row(
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        s.title.isEmpty ? '新对话' : s.title,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                          fontSize: 13,
                          fontWeight: FontWeight.w600,
                          color: widget.selected ? t.primaryInk : t.ink,
                        ),
                      ),
                      const SizedBox(height: 3),
                      Text(sub, style: TextStyle(fontSize: 11, color: t.faint)),
                    ],
                  ),
                ),
                // 隐藏不从树里摘：悬停进出行高不变，列表不跳。
                Opacity(
                  opacity: showActions ? 1 : 0,
                  child: IgnorePointer(
                    ignoring: !showActions,
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        _action(Icons.edit_outlined, '重命名', widget.onRename),
                        _action(
                            Icons.delete_outline, '删除会话', widget.onDelete),
                      ],
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
