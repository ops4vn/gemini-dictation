export namespace main {
	
	export class UpdateConfig {
	    url: string;
	    check_minutes: number;
	    auto_apply: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UpdateConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.check_minutes = source["check_minutes"];
	        this.auto_apply = source["auto_apply"];
	    }
	}
	export class Config {
	    api_key: string;
	    model: string;
	    disable_thinking: boolean;
	    hotkey: string;
	    hold_seconds: number;
	    sample_rate: number;
	    max_seconds: number;
	    min_seconds: number;
	    silence_rms: number;
	    sounds: boolean;
	    probe_mic_on_start: boolean;
	    trailing_space: boolean;
	    restore_clipboard: boolean;
	    web_port: number;
	    free_tier_limits: Record<string, any>;
	    update: UpdateConfig;
	    debug_keys: boolean;
	    onboarded: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.model = source["model"];
	        this.disable_thinking = source["disable_thinking"];
	        this.hotkey = source["hotkey"];
	        this.hold_seconds = source["hold_seconds"];
	        this.sample_rate = source["sample_rate"];
	        this.max_seconds = source["max_seconds"];
	        this.min_seconds = source["min_seconds"];
	        this.silence_rms = source["silence_rms"];
	        this.sounds = source["sounds"];
	        this.probe_mic_on_start = source["probe_mic_on_start"];
	        this.trailing_space = source["trailing_space"];
	        this.restore_clipboard = source["restore_clipboard"];
	        this.web_port = source["web_port"];
	        this.free_tier_limits = source["free_tier_limits"];
	        this.update = this.convertValues(source["update"], UpdateConfig);
	        this.debug_keys = source["debug_keys"];
	        this.onboarded = source["onboarded"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

