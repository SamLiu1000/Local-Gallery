// ============================================================
//  i18n — 国际化模块
//  ============================================================

const I18n = (() => {
    let _lang = 'zh-CN';
    let _dict = {};
    let _ready = false;
    const _listeners = [];

    async function init(lang) {
        _lang = lang || localStorage.getItem('lang') || 'zh-CN';
        await loadDict(_lang);
        _ready = true;
        scanDOM();
        _listeners.forEach(fn => fn(_lang));
    }

    async function loadDict(lang) {
        try {
            const resp = await fetch(`i18n/${lang}.json`);
            if (!resp.ok) throw new Error('HTTP ' + resp.status);
            _dict = await resp.json();
            document.documentElement.lang = lang;
            // RTL support for Arabic and other RTL languages
            const rtlLangs = ['ar', 'fa', 'he', 'ur'];
            const langPrefix = lang.split('-')[0];
            document.documentElement.dir = rtlLangs.includes(langPrefix) ? 'rtl' : 'ltr';
            localStorage.setItem('lang', lang);
        } catch (e) {
            console.warn('[I18n] 加载语言文件失败:', lang, e);
            if (lang !== 'zh-CN') {
                // 回退到中文
                _lang = 'zh-CN';
                return loadDict('zh-CN');
            }
            _dict = {};
        }
    }

    function t(key, params) {
        let s = _dict[key];
        if (s === undefined) {
            console.warn('[I18n] 缺少翻译:', key);
            return key;
        }
        if (params) {
            for (const [k, v] of Object.entries(params)) {
                s = s.replaceAll(`{${k}}`, v);
            }
        }
        return s;
    }

    function lang() { return _lang; }

    async function setLang(newLang) {
        if (newLang === _lang) return;
        await loadDict(newLang);
        _lang = newLang;
        scanDOM();
        _listeners.forEach(fn => fn(_lang));
    }

    /** 扫描 DOM 中所有 data-i18n 属性并替换文本 */
    function scanDOM() {
        // data-i18n → textContent
        document.querySelectorAll('[data-i18n]').forEach(el => {
            const key = el.getAttribute('data-i18n');
            const text = t(key);
            if (text !== key) el.textContent = text;
        });
        // data-i18n-title → title
        document.querySelectorAll('[data-i18n-title]').forEach(el => {
            const key = el.getAttribute('data-i18n-title');
            const text = t(key);
            if (text !== key) el.title = text;
        });
        // data-i18n-placeholder → placeholder
        document.querySelectorAll('[data-i18n-placeholder]').forEach(el => {
            const key = el.getAttribute('data-i18n-placeholder');
            const text = t(key);
            if (text !== key) el.placeholder = text;
        });
        // data-i18n-value → value (for option elements)
        document.querySelectorAll('[data-i18n-value]').forEach(el => {
            const key = el.getAttribute('data-i18n-value');
            const text = t(key);
            if (text !== key) el.value = text;
        });
    }

    /** 注册语言切换回调 */
    function onChange(fn) { _listeners.push(fn); }

    return { init, t, lang, setLang, scanDOM, onChange, get ready() { return _ready; } };
})();
