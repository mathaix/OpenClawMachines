// Stealth patches for headless Chromium anti-detection.
// Based on puppeteer-extra-plugin-stealth evasions.
// Injected via Manifest V3 content script with "world": "MAIN".

(function () {
  'use strict';

  // 1. navigator.webdriver — defense in depth with --disable-blink-features flag
  Object.defineProperty(navigator, 'webdriver', {
    get: () => false,
  });

  // 2. navigator.plugins — headless has empty array; real Chrome has these
  const makePlugin = (name, description, filename, mimeTypes) => {
    const plugin = { name, description, filename, length: mimeTypes.length };
    mimeTypes.forEach((mt, i) => {
      plugin[i] = mt;
    });
    return plugin;
  };

  const fakePluginArray = [
    makePlugin('Chrome PDF Plugin', 'Portable Document Format', 'internal-pdf-viewer', [
      { type: 'application/x-google-chrome-pdf', suffixes: 'pdf', description: 'Portable Document Format' },
    ]),
    makePlugin('Chrome PDF Viewer', '', 'mhjfbmdgcfjbbpaeojofohoefgiehjai', [
      { type: 'application/pdf', suffixes: 'pdf', description: '' },
    ]),
    makePlugin('Native Client', '', 'internal-nacl-plugin', [
      { type: 'application/x-nacl', suffixes: '', description: 'Native Client Executable' },
      { type: 'application/x-pnacl', suffixes: '', description: 'Portable Native Client Executable' },
    ]),
  ];
  fakePluginArray.item = (i) => fakePluginArray[i] || null;
  fakePluginArray.namedItem = (name) => fakePluginArray.find((p) => p.name === name) || null;
  fakePluginArray.refresh = () => {};

  Object.defineProperty(navigator, 'plugins', {
    get: () => fakePluginArray,
  });

  // 3. navigator.languages
  Object.defineProperty(navigator, 'languages', {
    get: () => ['en-US', 'en'],
  });

  // 4. navigator.vendor
  Object.defineProperty(navigator, 'vendor', {
    get: () => 'Google Inc.',
  });

  // 5. navigator.hardwareConcurrency — VM has 1 vCPU, report realistic value
  Object.defineProperty(navigator, 'hardwareConcurrency', {
    get: () => 4,
  });

  // 6. chrome.runtime — in non-extension contexts, exists but connect/sendMessage throw
  if (!window.chrome) window.chrome = {};
  if (!window.chrome.runtime) {
    window.chrome.runtime = {
      connect: function () {
        throw new Error('Could not establish connection. Receiving end does not exist.');
      },
      sendMessage: function () {
        throw new Error('Could not establish connection. Receiving end does not exist.');
      },
    };
  }

  // 7. chrome.app
  if (!window.chrome.app) {
    window.chrome.app = {
      isInstalled: false,
      InstallState: { DISABLED: 'disabled', INSTALLED: 'installed', NOT_INSTALLED: 'not_installed' },
      RunningState: { CANNOT_RUN: 'cannot_run', READY_TO_RUN: 'ready_to_run', RUNNING: 'running' },
      getDetails: function () {
        return null;
      },
      getIsInstalled: function () {
        return false;
      },
    };
  }

  // 8. chrome.csi
  if (!window.chrome.csi) {
    window.chrome.csi = function () {
      return {
        onloadT: Date.now(),
        startE: Date.now(),
        pageT: performance.now(),
        tran: 15,
      };
    };
  }

  // 9. chrome.loadTimes
  if (!window.chrome.loadTimes) {
    window.chrome.loadTimes = function () {
      var now = Date.now() / 1000;
      return {
        commitLoadTime: now,
        connectionInfo: 'h2',
        finishDocumentLoadTime: now,
        finishLoadTime: now,
        firstPaintAfterLoadTime: 0,
        firstPaintTime: now,
        navigationType: 'Other',
        npnNegotiatedProtocol: 'h2',
        requestTime: now - 0.16,
        startLoadTime: now - 0.16,
        wasAlternateProtocolAvailable: false,
        wasFetchedViaSpdy: true,
        wasNpnNegotiated: true,
      };
    };
  }

  // 10. WebGL vendor/renderer — report Intel instead of SwiftShader
  var getParamProto = WebGLRenderingContext.prototype.getParameter;
  WebGLRenderingContext.prototype.getParameter = function (param) {
    if (param === 37445) return 'Intel Inc.'; // UNMASKED_VENDOR_WEBGL
    if (param === 37446) return 'Intel Iris OpenGL Engine'; // UNMASKED_RENDERER_WEBGL
    return getParamProto.call(this, param);
  };
  if (typeof WebGL2RenderingContext !== 'undefined') {
    var getParam2Proto = WebGL2RenderingContext.prototype.getParameter;
    WebGL2RenderingContext.prototype.getParameter = function (param) {
      if (param === 37445) return 'Intel Inc.';
      if (param === 37446) return 'Intel Iris OpenGL Engine';
      return getParam2Proto.call(this, param);
    };
  }

  // 11. Permissions API — proper notification permission response
  if (typeof Permissions !== 'undefined' && Permissions.prototype.query) {
    var origQuery = Permissions.prototype.query;
    Permissions.prototype.query = function (parameters) {
      if (parameters.name === 'notifications') {
        return Promise.resolve({ state: Notification.permission });
      }
      return origQuery.call(this, parameters);
    };
  }

  // 12. window.outerWidth/outerHeight — headless sets these to 0
  Object.defineProperty(window, 'outerWidth', {
    get: function () {
      return window.innerWidth;
    },
  });
  Object.defineProperty(window, 'outerHeight', {
    get: function () {
      return window.innerHeight + 85;
    },
  });

  // 13. iframe.contentWindow — patch webdriver in child frames
  try {
    var iframeDesc = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'contentWindow');
    if (iframeDesc && iframeDesc.get) {
      Object.defineProperty(HTMLIFrameElement.prototype, 'contentWindow', {
        get: function () {
          var win = iframeDesc.get.call(this);
          if (!win) return win;
          try {
            Object.defineProperty(win.navigator, 'webdriver', { get: () => false });
          } catch (e) {
            /* cross-origin — expected */
          }
          return win;
        },
      });
    }
  } catch (e) {
    /* ignore if not supported */
  }
})();
