import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../theme.dart';
import 'provider_avatar.dart';

/// 摘要带的一项分类计数。colorKey 决定配色,与头像同一哈希色板,
/// 同一 key(如 deepseek)在头像和 chip 上颜色一致。
class BandStat {
  const BandStat({
    required this.label,
    required this.count,
    required this.colorKey,
  });

  final String label;
  final int count;
  final String colorKey;
}

/// 页头与列表之间的摘要带(CC Switch MCP 页式):统计 pill +
/// 分类计数彩色 chips + 标签式搜索框,白卡承载,是页面的装饰/信息空间。
class SummaryBand extends StatefulWidget {
  const SummaryBand({
    super.key,
    required this.summary,
    required this.stats,
    required this.searchHint,
    required this.onSearch,
    this.controller,
    this.clickableStats = false,
  });

  final String summary;
  final List<BandStat> stats;
  final String searchHint;

  /// 统计 chips 是否可点击:点击把标签加入/移出搜索关键字,
  /// 仅在页面搜索支持多关键字交集且标签本身可被搜索命中时开启。
  final bool clickableStats;

  /// 搜索词变化回调(关键字以空格连接,可能为空串)。
  final ValueChanged<String> onSearch;

  /// 可选外部搜索词通道:页面需要程序化改写搜索词时传入
  /// (如从账号页跳转过来并预填账号名);写入的文本按空白拆成
  /// 关键字标签。生命周期由外部负责。
  final TextEditingController? controller;

  @override
  State<SummaryBand> createState() => _SummaryBandState();
}

class _SummaryBandState extends State<SummaryBand> {
  /// 已落定的关键字,每个渲染为一个标签;onSearch 报它们的空格连接串。
  final List<String> _keywords = [];
  final TextEditingController _input = TextEditingController();

  /// 空输入时退格删除最后一个关键字。
  late final FocusNode _focus = FocusNode(onKeyEvent: (node, event) {
    if (event is KeyDownEvent &&
        event.logicalKey == LogicalKeyboardKey.backspace &&
        _input.text.isEmpty &&
        _keywords.isNotEmpty) {
      _removeKeyword(_keywords.length - 1);
      return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  });

  @override
  void initState() {
    super.initState();
    // 焦点变化要重绘搜索框描边
    _focus.addListener(() => setState(() {}));
    if (widget.controller != null) {
      _keywords.addAll(_split(widget.controller!.text));
      widget.controller!.addListener(_onExternal);
    }
  }

  @override
  void didUpdateWidget(SummaryBand oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.controller != widget.controller) {
      oldWidget.controller?.removeListener(_onExternal);
      _adoptExternal(widget.controller?.text);
      widget.controller?.addListener(_onExternal);
    }
  }

  @override
  void dispose() {
    widget.controller?.removeListener(_onExternal);
    _input.dispose();
    _focus.dispose();
    super.dispose();
  }

  void _onExternal() => _adoptExternal(widget.controller?.text);

  /// 外部通道写入新文本:按空白拆词整体替换当前关键字。
  void _adoptExternal(String? text) {
    final tokens = _split(text ?? '');
    if (_sameTokens(tokens, _keywords)) return;
    setState(() {
      _keywords
        ..clear()
        ..addAll(tokens);
      _input.clear();
    });
  }

  static List<String> _split(String text) => text
      .split(RegExp(r'\s+'))
      .where((t) => t.isNotEmpty)
      .toList();

  static bool _sameTokens(List<String> a, List<String> b) =>
      a.length == b.length &&
      List.generate(a.length, (i) => a[i].toLowerCase() == b[i].toLowerCase())
          .every((x) => x);

  void _notify() => widget.onSearch(_keywords.join(' '));

  /// 输入框内容落成关键字标签(遇空白或回车触发)。
  void _commitInput() {
    final tokens = _split(_input.text);
    if (tokens.isEmpty) return;
    var added = false;
    setState(() {
      for (final t in tokens) {
        if (!_keywords.any((k) => k.toLowerCase() == t.toLowerCase())) {
          _keywords.add(t);
          added = true;
        }
      }
      _input.clear();
    });
    if (added) _notify();
  }

  void _removeKeyword(int index) {
    setState(() => _keywords.removeAt(index));
    _notify();
  }

  /// 点统计 chip 把其标签加入/移出搜索关键字(交集搜索)。
  void _toggleKeyword(String label) {
    final i =
        _keywords.indexWhere((k) => k.toLowerCase() == label.toLowerCase());
    setState(() {
      if (i >= 0) {
        _keywords.removeAt(i);
      } else {
        _keywords.add(label);
      }
    });
    _notify();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final dark = Theme.of(context).brightness == Brightness.dark;
    return Container(
      margin: const EdgeInsets.fromLTRB(24, 2, 24, 12),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(AppConst.radiusCard),
      ),
      child: Column(
        children: [
          Row(
            children: [
              Container(
                padding:
                    const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                decoration: BoxDecoration(
                  color: t.primarySoft,
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Text(
                  widget.summary,
                  style: TextStyle(
                    fontSize: 12.5,
                    fontWeight: FontWeight.w600,
                    color: t.primaryInk,
                  ),
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: Wrap(
                  spacing: 8,
                  runSpacing: 6,
                  children: [
                    for (final s in widget.stats) _chip(s, dark),
                  ],
                ),
              ),
            ],
          ),
          const SizedBox(height: 10),
          _searchBox(t, dark),
        ],
      ),
    );
  }

  /// 标签式搜索框:已定关键字渲染为彩色标签,尾部跟随一个
  /// 自适应宽度的输入框;空白/回车落词,空输入退格删尾词。
  Widget _searchBox(AppTokens t, bool dark) {
    final focused = _focus.hasFocus;
    return GestureDetector(
      onTap: _focus.requestFocus,
      child: Container(
        constraints: const BoxConstraints(minHeight: 34),
        padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 3),
        decoration: BoxDecoration(
          color: t.bg,
          borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
          border: focused
              ? Border.all(color: t.primary, width: 1.5)
              : Border.all(color: Colors.transparent),
        ),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.center,
          children: [
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 6),
              child: Icon(Icons.search, size: 17, color: t.faint),
            ),
            Expanded(
              child: Wrap(
                spacing: 6,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  for (var i = 0; i < _keywords.length; i++)
                    _keywordChip(i, dark),
                  SizedBox(
                    width: _inputWidth(),
                    height: 24,
                    child: TextField(
                      controller: _input,
                      focusNode: _focus,
                      onChanged: (v) {
                        if (RegExp(r'\s').hasMatch(v)) {
                          _commitInput();
                        } else {
                          setState(() {});
                        }
                      },
                      onSubmitted: (_) {
                        _commitInput();
                        _focus.requestFocus();
                      },
                      style: const TextStyle(fontSize: 13),
                      decoration: InputDecoration(
                        isDense: true,
                        // 主题给输入框默认带填充和描边,这里全部剥掉,
                        // 让标签与输入共享外层容器这一个"编辑框"
                        filled: false,
                        border: InputBorder.none,
                        enabledBorder: InputBorder.none,
                        focusedBorder: InputBorder.none,
                        errorBorder: InputBorder.none,
                        disabledBorder: InputBorder.none,
                        contentPadding: EdgeInsets.zero,
                        hintText: _keywords.isEmpty ? widget.searchHint : null,
                        hintStyle: TextStyle(fontSize: 12.5, color: t.faint),
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  /// 输入框宽度随已输入文本增长;无关键字时给足 hint 全长的宽度。
  double _inputWidth() {
    final painter = TextPainter(
      text: TextSpan(text: _input.text, style: const TextStyle(fontSize: 13)),
      textDirection: TextDirection.ltr,
    )..layout();
    return math.max(_keywords.isEmpty ? 420.0 : 160.0, painter.width + 24);
  }

  Widget _keywordChip(int index, bool dark) {
    final kw = _keywords[index];
    final (bg, fg) = ProviderAvatar.colorsFor(kw, dark: dark);
    return MouseRegion(
      cursor: SystemMouseCursors.click,
      child: GestureDetector(
        onTap: () => _removeKeyword(index),
        child: Tooltip(
          message: '点击移出关键字',
          child: Container(
            padding: const EdgeInsets.fromLTRB(8, 3, 9, 2),
            decoration: BoxDecoration(
              color: bg,
              borderRadius: BorderRadius.circular(7),
            ),
            child: Stack(
              clipBehavior: Clip.none,
              children: [
                Text(
                  kw,
                  style: TextStyle(
                    fontSize: 11.5,
                    fontWeight: FontWeight.w500,
                    color: fg,
                  ),
                ),
                Positioned(
                  top: -4,
                  right: -7,
                  child: Icon(Icons.close, size: 9, color: fg),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _chip(BandStat s, bool dark) {
    final (bg, fg) = ProviderAvatar.colorsFor(s.colorKey, dark: dark);
    if (!widget.clickableStats) {
      return Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
        decoration: BoxDecoration(
          color: bg,
          borderRadius: BorderRadius.circular(7),
        ),
        child: Text(
          '${s.label}: ${s.count}',
          style: TextStyle(
            fontSize: 11.5,
            fontWeight: FontWeight.w500,
            color: fg,
          ),
        ),
      );
    }
    final active =
        _keywords.any((k) => k.toLowerCase() == s.label.toLowerCase());
    return MouseRegion(
      cursor: SystemMouseCursors.click,
      child: GestureDetector(
        onTap: () => _toggleKeyword(s.label),
        child: Tooltip(
          message: active ? '点击移出搜索关键字' : '点击加入搜索关键字',
          child: Container(
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
            decoration: BoxDecoration(
              color: bg,
              borderRadius: BorderRadius.circular(7),
              border: Border.all(
                color: active ? fg : Colors.transparent,
              ),
            ),
            child: Text(
              '${s.label}: ${s.count}',
              style: TextStyle(
                fontSize: 11.5,
                fontWeight: FontWeight.w500,
                color: fg,
              ),
            ),
          ),
        ),
      ),
    );
  }
}
