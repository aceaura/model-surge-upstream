import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../theme.dart';

/// 移植自 fengshen-slicer 的自绘下拉:触发器复用输入框外壳(labelText 与
/// TextField 同款浮动 label:showValue=false 时占位显示在框内,打开菜单或选中
/// 子项后浮到框缘),菜单落在触发器正下方(间距 6,不遮挡触发器),surface 底
/// radius 12 + 1px 浅描边 + 投影,内缩 primarySoft 圆角 8 选中块 +
/// primaryInk 加粗 + 主色 check。
class StyledDropdown extends StatefulWidget {
  final String? value;
  final List<String> options;
  final ValueChanged<String?> onChanged;
  final InputDecoration? decoration;
  final String Function(String)? labelOf;
  // false=未选占位态:框内显示 label,值隐藏;true=显示值,label 浮在框缘
  final bool showValue;
  // 菜单朝触发器上方展开:触发器贴近窗口底缘(如对话页输入栏)时
  // 默认的下翻菜单会落到窗口外,既看不见也点不到。
  final bool dropUp;
  // false=禁用:不响应点击(级联选择中下级等上级选定后再解锁)。
  final bool enabled;
  // true=触发器按最长选项文案自适应宽(页头过滤器用),
  // 选中切换不跳宽;false 保持父级约束(表单内拉满)。
  final bool fitContent;
  const StyledDropdown(
      {super.key,
      required this.value,
      required this.options,
      required this.onChanged,
      this.decoration,
      this.labelOf,
      this.showValue = true,
      this.dropUp = false,
      this.enabled = true,
      this.fitContent = false});

  @override
  State<StyledDropdown> createState() => _StyledDropdownState();
}

class _StyledDropdownState extends State<StyledDropdown> {
  final LayerLink _link = LayerLink();
  final GlobalKey _triggerKey = GlobalKey();
  OverlayEntry? _entry;

  String _label(String o) => widget.labelOf == null ? o : widget.labelOf!(o);

  bool get _open => _entry != null;

  void _toggle() => _open ? _close() : _show();

  /// 最长选项文案宽(按菜单选中项加粗 w600 量),供菜单宽用。
  /// textScaler 必须与渲染同源:系统文字缩放(Windows 文本大小)下
  /// 不传会量小一截,菜单装不下文案出省略号。
  double _maxOptionTextWidth() {
    var maxText = 0.0;
    for (final o in widget.options) {
      final tp = TextPainter(
        text: TextSpan(
            text: _label(o),
            style: TextStyle(
                fontSize: 13,
                fontWeight: FontWeight.w600,
                fontFamily: AppConst.fontFamily,
                fontFamilyFallback: AppConst.fontFallback)),
        maxLines: 1,
        textDirection: TextDirection.ltr,
        textScaler: MediaQuery.textScalerOf(context),
      )..layout();
      maxText = math.max(maxText, tp.width);
      tp.dispose();
    }
    return maxText;
  }

  /// 自适应触发器宽:最长选项文案按触发器实际渲染样式量(正文 w400 +
  /// DefaultTextStyle 继承的字距,textScaler 同源;此前按 w600 无字距量,
  /// 与渲染不一致把「全部来源」这类全 CJK 文案量小出省略号)。
  /// 常量 = 横向 padding 26 + inputGap 2x4(M3 下 OutlineInputBorder 的
  /// gapPadding 计入装饰器内容槽两侧扣减,见 input_decorator contentConstraints)
  /// + 箭头 18 + 2 取整兜底,封顶与菜单一致。
  double _fitWidth(TextStyle triggerStyle) {
    final merged = DefaultTextStyle.of(context).style.merge(triggerStyle);
    final scaler = MediaQuery.textScalerOf(context);
    var maxText = 0.0;
    for (final o in widget.options) {
      final tp = TextPainter(
        text: TextSpan(text: _label(o), style: merged),
        maxLines: 1,
        textDirection: TextDirection.ltr,
        textScaler: scaler,
      )..layout();
      maxText = math.max(maxText, tp.width);
      tp.dispose();
    }
    return math.min(maxText + 54, 420.0);
  }

  void _show() {
    final box = _triggerKey.currentContext!.findRenderObject() as RenderBox;
    final t = context.tokens;
    final dark = Theme.of(context).brightness == Brightness.dark;
    // 菜单高度可算:子项定高 38 + 内边距 12,封顶与 maxHeight 一致。
    // 上翻时按它把菜单底缘贴到触发器顶缘上方 6px。
    final menuHeight =
        math.min(360.0, widget.options.length * 38.0 + 12.0);
    final dy = widget.dropUp
        ? -(menuHeight + 6.0)
        : box.size.height + 6.0;
    // 菜单宽取触发器宽与选项自然宽的较大者(封顶 420):选项比触发器长
    // (如模型 id codex-1/gpt-6.1-sol 配 200px 触发器)时完整显示,
    // 行内省略只作更极端值的兜底。按 w600 量:选中项加粗,取最宽。
    final maxText = _maxOptionTextWidth();
    // 行横向 padding 20 + check 位 24 + 菜单 padding 12 + 描边 2。
    final menuWidth = math.max(
        box.size.width, math.min(maxText + 58, 420.0));
    _entry = OverlayEntry(
      builder: (_) => Stack(children: [
        // 透明屏障:点外部收菜单
        Positioned.fill(
          child: GestureDetector(
            onTap: _close,
            behavior: HitTestBehavior.translucent,
            child: const SizedBox.expand(),
          ),
        ),
        CompositedTransformFollower(
          link: _link,
          showWhenUnlinked: false,
          offset: Offset(0, dy),
          child: Align(
            alignment: Alignment.topLeft,
            // 底色必须画在 Material 上:InkWell 的 hover 墨水绘在最近 Material 层,
            // 若底色由子级 Container 画会盖住墨水,悬停永远不可见
            child: Container(
              width: menuWidth,
              constraints: const BoxConstraints(maxHeight: 360),
              decoration: BoxDecoration(
                borderRadius: BorderRadius.circular(12),
                border: Border.all(
                    color: dark ? t.border : const Color(0xFFE3E7EE)),
                boxShadow: [
                  BoxShadow(
                    color: dark
                        ? Colors.black54
                        : const Color.fromRGBO(20, 30, 60, .16),
                    blurRadius: 24,
                    offset: const Offset(0, 6),
                  ),
                ],
              ),
              child: Material(
                color: t.surface,
                borderRadius: BorderRadius.circular(12),
                clipBehavior: Clip.antiAlias,
                child: Padding(
                  padding: const EdgeInsets.all(6),
                  child: SingleChildScrollView(
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      children: [for (final o in widget.options) _item(t, o)],
                    ),
                  ),
                ),
              ),
            ),
          ),
        ),
      ]),
    );
    Overlay.of(context).insert(_entry!);
    setState(() {}); // _open 转真:聚焦描边与浮标(InputDecorator 自带过渡)
  }

  Widget _item(AppTokens t, String o) {
    final sel = o == widget.value;
    // 上下各 2px 空隙:相邻选中/悬停色块不互贴
    return SizedBox(
      height: 38,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 2),
        child: InkResponse(
          borderRadius: BorderRadius.circular(8),
          highlightShape: BoxShape.rectangle,
          hoverColor: t.primarySoft,
          splashColor: Colors.transparent,
          highlightColor: Colors.transparent,
          onTap: () {
            _close();
            widget.onChanged(o);
          },
          child: Container(
            padding: const EdgeInsets.symmetric(horizontal: 10),
            decoration: sel
                ? BoxDecoration(
                    color: t.primarySoft, borderRadius: BorderRadius.circular(8))
                : null,
            child: Row(children: [
              // 菜单已按选项自然宽展开,这里的省略只是超 420 封顶的兜底
              Expanded(
                child: Text(_label(o),
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      fontSize: 13,
                      color: sel ? t.primaryInk : t.ink,
                      fontWeight: sel ? FontWeight.w600 : null,
                      fontFamily: AppConst.fontFamily,
                      fontFamilyFallback: AppConst.fontFallback,
                    )),
              ),
              if (sel) Icon(Icons.check, size: 15, color: t.primary),
            ]),
          ),
        ),
      ),
    );
  }

  void _close() {
    if (_entry == null) return;
    _entry!.remove();
    _entry = null;
    setState(() {}); // 触发器聚焦描边与浮标随菜单开合还原
  }

  @override
  void dispose() {
    _entry?.remove();
    _entry = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final d = widget.decoration ?? const InputDecoration();
    // label 交给 InputDecorator 原生浮动(与 TextField 同机制:字号/居中/缺口/
    // 高度全自动一致)。showValue=false 视为空(占位 label 在框内);打开菜单
    // (isFocused)或已选子项(showValue=true)时浮到框缘
    final showContent = widget.showValue || _open;
    final current = widget.value;
    final triggerStyle = TextStyle(
        fontSize: 13,
        color: widget.enabled ? t.ink : t.faint,
        fontFamily: AppConst.fontFamily,
        fontFamilyFallback: AppConst.fontFallback);
    final trigger = CompositedTransformTarget(
      link: _link,
      child: MouseRegion(
        cursor: widget.enabled
            ? SystemMouseCursors.click
            : SystemMouseCursors.basic,
        child: GestureDetector(
          key: _triggerKey,
          behavior: HitTestBehavior.opaque,
          onTap: widget.enabled ? _toggle : null,
          child: InputDecorator(
            decoration: d,
            isEmpty: !widget.showValue,
            isFocused: _open,
            // 定高:值显隐切换时盒子不缩;内容高与 TextFormField 正文行高
            // (fontSize 16 x height 1.5 = 24)一致,触发器总高与输入框相同
            child: SizedBox(
              height: 24,
              // 箭头常驻(含占位态);只值文本按态显隐
              child: Row(children: [
                Expanded(
                  child: !showContent
                      ? const SizedBox.shrink()
                      : Text(
                          current == null ? '' : _label(current),
                          overflow: TextOverflow.ellipsis,
                          // 显式钉字体族:textStyle 缺 fontFamily 会在合并链上丢掉字体栈
                          style: triggerStyle,
                        ),
                ),
                Icon(Icons.expand_more_rounded,
                    size: 18, color: widget.enabled ? t.dim : t.faint),
              ]),
            ),
          ),
        ),
      ),
    );
    // 自适应模式:自带宽度不再吃父级约束(InputDecorator 断言不能无界宽,
    // 不能靠内容自撑,必须给定宽)
    if (widget.fitContent) {
      return SizedBox(width: _fitWidth(triggerStyle), child: trigger);
    }
    return trigger;
  }
}

/// 带 Form 校验的 StyledDropdown:errorText 走 InputDecorator 原生错误描边/文案。
class StyledDropdownFormField extends FormField<String> {
  StyledDropdownFormField({
    super.key,
    required String? value,
    required List<String> options,
    required ValueChanged<String?> onChanged,
    InputDecoration decoration = const InputDecoration(),
    String Function(String)? labelOf,
    bool enabled = true,
    super.validator,
  }) : super(
          initialValue: value,
          builder: (field) => StyledDropdown(
            value: field.value,
            options: options,
            labelOf: labelOf,
            showValue: field.value != null,
            enabled: enabled,
            decoration:
                decoration.copyWith(errorText: field.errorText, enabled: enabled),
            onChanged: (v) {
              field.didChange(v);
              onChanged(v);
            },
          ),
        );

  @override
  FormFieldState<String> createState() => _StyledDropdownFormFieldState();
}

/// FormField 基类的 didUpdateWidget 不回同步 initialValue,父级从外部改值
/// (级联换上级重置下级)时下拉会残留旧值;这里补上外部值变更的回同步。
class _StyledDropdownFormFieldState extends FormFieldState<String> {
  @override
  void didUpdateWidget(StyledDropdownFormField oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.initialValue != oldWidget.initialValue) {
      setValue(widget.initialValue);
    }
  }
}
