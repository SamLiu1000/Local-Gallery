/* ============================================================
   api.js - 大模型 API 调用模块
    支持 OpenAI 兼容 API（本地/第三方大模型）
    用于提示词反推和再生成
    支持自定义提示词模板和代理设置
    ============================================================ */

const ApiService = (() => {

    let currentRequestId = null;

    // ==================== 默认提示词模板 ====================

    const DEFAULT_SYSTEM_PROMPT = `You are an expert AI image prompt analyst. Analyze the given image and provide:
1. A detailed positive prompt that would generate this image (describe subject, style, composition, lighting, color palette, mood, quality tags)
2. A negative prompt listing what should be avoided

Format your response as JSON:
{
  "positivePrompt": "detailed positive prompt here",
  "negativePrompt": "negative prompt here"
}

For the positive prompt: Include artistic style, subject details, composition, lighting, color scheme, quality tags (like "masterpiece, best quality, 8k, highly detailed").
For the negative prompt: Include common negatives (like "low quality, blurry, distorted, bad anatomy, watermark, text").

Respond ONLY with the JSON object, no other text.`;

    const DEFAULT_USER_PROMPT = 'Please analyze this AI-generated image and provide the prompt that would recreate it. Return ONLY a JSON object with positivePrompt and negativePrompt fields.';

    // ==================== 核心调用 ====================

    /**
     * 统一的 fetch 封装，支持代理
     * 代理模式有两种：
     *   1. 外部代理（如 clash/v2ray）：直接请求代理服务器的 /proxy 端点
     *   2. 本地开发服务器代理：请求本机 localhost:8080/proxy，由服务器转发
     */
    async function apiFetch(url, options, config) {
        // 兼容两种代理配置格式：嵌套 config.proxy 和扁平 config.proxyEnabled
        const proxyEnabled = config?.proxy?.enabled || config?.proxyEnabled;
        const proxyHost = config?.proxy?.host || config?.proxyHost;
        const proxyPort = config?.proxy?.port || config?.proxyPort;
        const proxyProtocol = config?.proxy?.protocol || config?.proxyProtocol || 'http';

        // 如果启用了代理
        if (config && proxyEnabled && proxyHost && proxyPort) {
            const protocol = proxyProtocol;
            const proxyBase = `${protocol}://${proxyHost}:${proxyPort}`;
            
            // 构建代理请求体：将原始请求信息打包，包含代理配置
            const requestId = 'req_' + Date.now() + '_' + Math.random().toString(36).substr(2, 9);
            currentRequestId = requestId;
            const proxyBody = {
                requestId: requestId,
                url: url,
                method: options.method || 'GET',
                headers: options.headers || {},
                body: options.body || null,
                proxyHost: proxyHost || '',
                proxyPort: proxyPort || 0
            };

            // ★ Wails 环境：通过 WailsBridge 代理请求
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                if (options.signal && options.signal.aborted) {
                    currentRequestId = null;
                    throw new DOMException('已取消', 'AbortError');
                }
                // 监听 AbortSignal：一旦触发立即取消 Go 层请求
                const onAbort = () => {
                    if (currentRequestId) {
                        WailsBridge.cancelProxyRequest(currentRequestId);
                        currentRequestId = null;
                    }
                };
                if (options.signal) {
                    options.signal.addEventListener('abort', onAbort, { once: true });
                }
                const result = await WailsBridge.proxyRequest(proxyBody);
                currentRequestId = null;
                if (options.signal) {
                    options.signal.removeEventListener('abort', onAbort);
                }
                if (options.signal && options.signal.aborted) {
                    throw new DOMException('已取消', 'AbortError');
                }
                if (result.statusCode < 200 || result.statusCode >= 300) {
                    throw new Error(`代理请求失败 (${result.statusCode}): ${result.body}`);
                }
                // 构造一个类 Response 对象
                if (!result.body || result.body.trim() === '') {
                    throw new Error(`API 返回空响应 (HTTP ${result.statusCode})，请检查模型是否已加载`);
                }
                return {
                    ok: result.statusCode >= 200 && result.statusCode < 300,
                    status: result.statusCode,
                    json: async () => {
                        try { return JSON.parse(result.body); } catch (e) {
                            throw new Error(`API 返回了非 JSON 格式的数据 (HTTP ${result.statusCode})，响应预览: ${result.body.substring(0, 100)}`);
                        }
                    },
                    text: async () => result.body
                };
            }

            currentRequestId = null; // 非 Wails 路径，无需 Go 层取消

            // 判断是否使用本地开发服务器的代理端点
            const localProxyUrl = `${window.location.protocol}//${window.location.host}/proxy`;
            
            // 尝试通过本地服务器代理转发
            try {
                const proxyResponse = await fetch(localProxyUrl, {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify(proxyBody)
                });

                if (!proxyResponse.ok) {
                    const errorText = await proxyResponse.text();
                    throw new Error(`代理请求失败 (${proxyResponse.status}): ${errorText}`);
                }

                return proxyResponse;
            } catch (err) {
                if (err.message.includes('Failed to fetch') || err.name === 'TypeError') {
                    console.warn('[API] 本地服务器代理不可用，尝试直接请求代理服务器');
                    const proxyUrl = `${proxyBase}/proxy`;
                    const proxyResponse = await fetch(proxyUrl, {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                        body: JSON.stringify(proxyBody)
                    });

                    if (!proxyResponse.ok) {
                        const errorText = await proxyResponse.text();
                        throw new Error(`代理请求失败 (${proxyResponse.status}): ${errorText}`);
                    }

                    return proxyResponse;
                }
                throw err;
            }
        }

        // 无代理，直接请求
        return fetch(url, options);
    }

    /**
     * 压缩图片到指定最大尺寸和质量，减少 Base64 数据量
     * @param {string} dataUrl - 图片的 data URL
     * @param {number} maxWidth - 最大宽度（默认 1024）
     * @param {number} quality - JPEG 质量 0-1（默认 0.8）
     * @returns {Promise<string>} 压缩后的 data URL
     */
    function compressImage(dataUrl, maxWidth = 1024, quality = 0.8) {
        return new Promise((resolve, reject) => {
            const img = new Image();
            img.onload = () => {
                // 计算缩放尺寸，保持宽高比
                let width = img.width;
                let height = img.height;
                if (width > maxWidth) {
                    height = Math.round(height * (maxWidth / width));
                    width = maxWidth;
                }
                // 如果高度仍然太大（比如长图），也限制一下
                const maxHeight = maxWidth * 1.5;
                if (height > maxHeight) {
                    width = Math.round(width * (maxHeight / height));
                    height = maxHeight;
                }

                const canvas = document.createElement('canvas');
                canvas.width = width;
                canvas.height = height;
                const ctx = canvas.getContext('2d');
                ctx.drawImage(img, 0, 0, width, height);
                // 输出为 JPEG 以减小体积
                resolve(canvas.toDataURL('image/jpeg', quality));
            };
            img.onerror = () => reject(new Error('图片加载失败，无法压缩'));
            img.src = dataUrl;
        });
    }

    /**
     * 调用大模型 API 进行图片提示词反推
     * @param {string} imageBase64 - 图片的 Base64 编码（可带 data: 前缀）
     * @param {Object} config - API 配置对象
     * @param {string} config.baseUrl - API Base URL
     * @param {string} config.apiKey - API Key
     * @param {string} config.model - 模型名称
     * @param {number} config.temperature - Temperature
     * @param {number} config.maxTokens - Max Tokens
     * @param {string} config.systemPrompt - 自定义系统提示词（可选）
     * @param {string} config.userPrompt - 自定义用户提示词（可选）
     * @param {boolean} config.proxyEnabled - 是否启用代理
     * @param {string} config.proxyProtocol - 代理协议 http/https
     * @param {string} config.proxyHost - 代理地址
     * @param {number} config.proxyPort - 代理端口
     * @returns {Promise<{positivePrompt: string, negativePrompt: string}>}
     */
    async function reversePrompt(imageBase64, config, externalSignal) {
        if (!config || !config.baseUrl || !config.model) {
            throw new Error('API 配置不完整，请先配置 API 参数');
        }

        // 用户配置的超时时间（默认 120 秒）
        const timeoutSec = config.timeout || 120;
        const maxRetries = 3;
        const retryDelay = 5000;

        // 压缩图片（只做一次，重试时复用）
        let imageUrl = imageBase64.startsWith('data:')
            ? imageBase64
            : `data:image/png;base64,${imageBase64}`;
        try {
            imageUrl = await compressImage(imageUrl, 1024, 0.8);
        } catch (e) {
            console.warn('[API] 图片压缩失败，使用原图:', e.message);
        }

        // 使用自定义提示词或默认提示词
        const systemPrompt = config.systemPrompt || DEFAULT_SYSTEM_PROMPT;
        const userPromptText = config.userPrompt || DEFAULT_USER_PROMPT;

        const url = normalizeUrl(config.baseUrl) + '/chat/completions';

        const headers = { 'Content-Type': 'application/json' };
        if (config.apiKey) {
            headers['Authorization'] = `Bearer ${config.apiKey}`;
        }

        const requestBody = {
            model: config.model,
            messages: [
                { role: 'system', content: systemPrompt },
                {
                    role: 'user',
                    content: [
                        { type: 'text', text: userPromptText },
                        { type: 'image_url', image_url: { url: imageUrl } }
                    ]
                }
            ],
            temperature: config.temperature || 0.7,
            max_tokens: config.maxTokens || 2048
        };

        let lastError = null;
        for (let attempt = 0; attempt < maxRetries; attempt++) {
            // 检查外部取消信号
            if (externalSignal && externalSignal.aborted) {
                throw new Error('已取消');
            }

            // 每次尝试创建独立的超时控制器
            const controller = new AbortController();
            const timeoutId = setTimeout(() => controller.abort(), timeoutSec * 1000);

            const onExternalAbort = () => {
                controller.abort();
                clearTimeout(timeoutId);
            };
            if (externalSignal) {
                externalSignal.addEventListener('abort', onExternalAbort, { once: true });
            }

            try {
                const response = await apiFetch(url, {
                    method: 'POST',
                    headers,
                    body: JSON.stringify(requestBody),
                    signal: controller.signal
                }, config);

                clearTimeout(timeoutId);
                if (externalSignal) externalSignal.removeEventListener('abort', onExternalAbort);

                if (!response.ok) {
                    const errorText = await response.text();
                    const status = response.status;
                    // 非瞬时错误不重试
                    if (status === 401 || status === 403 || status === 404) {
                        throw new Error(`API 请求失败 (${status}): ${errorText}`);
                    }
                    // 服务端错误（5xx / 429）可重试
                    if (status >= 500 || status === 429) {
                        throw new Error(`[retryable] API 请求失败 (${status}): ${errorText}`);
                    }
                    throw new Error(`API 请求失败 (${status}): ${errorText}`);
                }

                const data = await response.json();
                let content = data.choices?.[0]?.message?.content || '';

                if (!content) {
                    throw new Error('API 返回了空响应，请检查模型是否支持图片分析');
                }

                // 移除 <think> 推理标签（DeepSeek-R1 / QwQ）
                if (config.stripThinking) {
                    content = stripThinking(content);
                }

                // 解析 JSON 响应
                try {
                    const jsonMatch = content.match(/\{[\s\S]*\}/);
                    const jsonStr = jsonMatch ? jsonMatch[0] : content;
                    const parsed = JSON.parse(jsonStr);
                    return {
                        positivePrompt: parsed.positivePrompt || parsed.positive_prompt || parsed.prompt || '',
                        negativePrompt: parsed.negativePrompt || parsed.negative_prompt || ''
                    };
                } catch (e) {
                    console.warn('[API] JSON 解析失败，尝试文本提取:', content.substring(0, 200));
                    return extractPromptsFromText(content);
                }
            } catch (err) {
                clearTimeout(timeoutId);
                if (externalSignal) externalSignal.removeEventListener('abort', onExternalAbort);

                // 用户主动取消
                if (err.message === '已取消' || (externalSignal && externalSignal.aborted)) {
                    throw new Error('已取消');
                }

                lastError = err;
                const msg = err.message || '';

                // 判断是否可重试
                const isRetryable =
                    msg.startsWith('[retryable]') ||
                    err.name === 'AbortError' ||
                    msg.includes('Failed to fetch') ||
                    msg.includes('TypeError') ||
                    msg.includes('timeout') ||
                    msg.includes('NetworkError') ||
                    msg.includes('network');

                if (isRetryable && attempt < maxRetries - 1) {
                    console.warn(`[API] 第 ${attempt + 1} 次尝试失败，${retryDelay / 1000}s 后重试:`, msg);
                    await new Promise(resolve => setTimeout(resolve, retryDelay));
                    continue;
                }

                // 最后一次尝试也失败了
                if (err.name === 'AbortError') {
                    throw new Error('请求超时，图片可能过大或模型响应过慢');
                }
                if (msg.startsWith('[retryable]')) {
                    throw new Error(msg.replace('[retryable] ', ''));
                }
                throw new Error(`网络请求失败: ${msg}。请检查 API 地址是否正确、服务是否运行、以及是否有 CORS 限制`);
            }
        }

        throw lastError || new Error('未知错误');
    }

    /**
     * 移除 <think> 推理标签（DeepSeek-R1 / QwQ 等模型）
     */
    function stripThinking(text) {
        return text.replace(/<think>[\s\S]*?<\/think>/gi, '').trim();
    }

    /**
     * 测试 API 连接并获取可用模型列表
     * @param {Object} config
     * @returns {Promise<{success: boolean, models: string[]}>}
     */
    async function testConnection(config) {
        if (!config || !config.baseUrl) {
            throw new Error('API Base URL 不能为空');
        }

        const url = normalizeUrl(config.baseUrl) + '/models';

        const headers = {};
        if (config.apiKey) {
            headers['Authorization'] = `Bearer ${config.apiKey}`;
        }

        const response = await apiFetch(url, { headers }, config);

        if (!response.ok) {
            const errorText = await response.text();
            throw new Error(`连接失败 (${response.status}): ${errorText}`);
        }

        const data = await response.json();
        const models = (data.data || []).map(m => m.id || '').filter(Boolean);
        return { success: models.length > 0, models };
    }

    /**
     * 使用大模型生成图片（文生图）
     * @param {string} prompt - 提示词
     * @param {string} negativePrompt - 负面提示词
     * @param {Object} config - API 配置
     * @returns {Promise<string>} Base64 图片数据
     */
    async function generateImage(prompt, negativePrompt, config) {
        if (!config || !config.baseUrl || !config.model) {
            throw new Error('API 配置不完整');
        }

        const url = normalizeUrl(config.baseUrl) + '/images/generations';

        const fullPrompt = negativePrompt
            ? `${prompt}\nNegative prompt: ${negativePrompt}`
            : prompt;

        const requestBody = {
            model: config.model,
            prompt: fullPrompt,
            n: 1,
            size: '1024x1024',
            response_format: 'b64_json'
        };

        const headers = {
            'Content-Type': 'application/json'
        };
        if (config.apiKey) {
            headers['Authorization'] = `Bearer ${config.apiKey}`;
        }

        const response = await apiFetch(url, {
            method: 'POST',
            headers,
            body: JSON.stringify(requestBody)
        }, config);

        if (!response.ok) {
            const errorText = await response.text();
            throw new Error(`图片生成失败 (${response.status}): ${errorText}`);
        }

        const data = await response.json();
        return data.data?.[0]?.b64_json || '';
    }

    // ==================== 辅助函数 ====================

    function normalizeUrl(url) {
        // 移除尾部斜杠
        let normalized = url.trim().replace(/\/+$/, '');
        return normalized;
    }

    function extractPromptsFromText(text) {
        const result = { positivePrompt: '', negativePrompt: '' };

        // 尝试匹配 "Positive Prompt:" / "Negative Prompt:" 模式
        const posMatch = text.match(/(?:Positive\s*Prompt|Prompt)[:\s]*([\s\S]*?)(?=(?:Negative\s*Prompt|$))/i);
        const negMatch = text.match(/(?:Negative\s*Prompt)[:\s]*([\s\S]*?)$/i);

        if (posMatch) result.positivePrompt = posMatch[1].trim();
        if (negMatch) result.negativePrompt = negMatch[1].trim();

        // 如果没匹配到，把整个文本作为 positive prompt
        if (!result.positivePrompt && !result.negativePrompt) {
            result.positivePrompt = text.trim();
        }

        return result;
    }

    /**
     * 将图片 File 转换为 Base64
     */
    function fileToBase64(file) {
        return new Promise((resolve, reject) => {
            const reader = new FileReader();
            reader.onload = () => {
                const result = reader.result;
                // 移除 data:image/...;base64, 前缀
                const base64 = result.split(',')[1] || result;
                resolve(base64);
            };
            reader.onerror = reject;
            reader.readAsDataURL(file);
        });
    }

    /**
     * 从 URL 获取图片并转为 Base64
     * 支持 blob: URL、data: URL 和普通 HTTP URL
     */
    async function urlToBase64(url) {
        // 如果是 data URL，直接返回
        if (url.startsWith('data:')) {
            return url.split(',')[1] || url;
        }

        // 如果是 blob URL，使用 fetch + FileReader 读取
        if (url.startsWith('blob:')) {
            try {
                const response = await fetch(url);
                const blob = await response.blob();
                return new Promise((resolve, reject) => {
                    const reader = new FileReader();
                    reader.onload = () => {
                        const result = reader.result;
                        const base64 = result.split(',')[1] || result;
                        resolve(base64);
                    };
                    reader.onerror = reject;
                    reader.readAsDataURL(blob);
                });
            } catch (err) {
                throw new Error(`无法读取图片: ${err.message}。请尝试直接选择图片文件`);
            }
        }

        // HTTP(S) URL - 使用 fetch 获取，避免 Canvas 的 CORS 污染问题
        try {
            const response = await fetch(url);
            if (!response.ok) {
                throw new Error(`HTTP ${response.status}`);
            }
            const blob = await response.blob();
            return new Promise((resolve, reject) => {
                const reader = new FileReader();
                reader.onload = () => {
                    const result = reader.result;
                    const base64 = result.split(',')[1] || result;
                    resolve(base64);
                };
                reader.onerror = () => reject(new Error('Base64 转换失败'));
                reader.readAsDataURL(blob);
            });
        } catch (err) {
            throw new Error(`图片加载失败: ${err.message}`);
        }
    }

    function cancelCurrentRequest() {
        if (currentRequestId && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            WailsBridge.cancelProxyRequest(currentRequestId);
            currentRequestId = null;
        }
    }

    // ==================== 公开 API ====================

    return {
        reversePrompt,
        testConnection,
        generateImage,
        fileToBase64,
        urlToBase64,
        cancelCurrentRequest
    };
})();
