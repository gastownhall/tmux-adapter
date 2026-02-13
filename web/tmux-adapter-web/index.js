import { TmuxAdapterWeb } from './tmux-adapter-web.js';
export { TmuxAdapterWeb } from './tmux-adapter-web.js';
export { encodeBinaryFrame, decodeBinaryFrame, BinaryMsgType } from './protocol.js';

customElements.define('tmux-adapter-web', TmuxAdapterWeb);
