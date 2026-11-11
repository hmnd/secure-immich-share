package sanitize

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// The verifier's allowlist: every group:tag exiftool reports must match one
// of these patterns (path.Match syntax), or the file is rejected. Tags with
// free-form values carry a value check.
//
// Patterns are kept per container family so a tag that is structural in one
// format can't pass in another.

type rule struct {
	pattern string
	// value, when set, must accept the tag's value.
	value func(any) bool
}

func allow(patterns ...string) []rule {
	out := make([]rule, len(patterns))
	for i, p := range patterns {
		out[i] = rule{pattern: p}
	}
	return out
}

func matchesRe(expr string) func(any) bool {
	re := regexp.MustCompile(expr)
	return func(v any) bool {
		s, ok := scalar(v)
		return ok && re.MatchString(s)
	}
}

func oneOf(vals ...string) func(any) bool {
	return func(v any) bool {
		s, ok := v.(string)
		return ok && slices.Contains(vals, s)
	}
}

// scalar returns a string or number value as text.
func scalar(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	}
	return "", false
}

const zeroDate = "0000:00:00 00:00:00"

var common = allow(
	"ExifTool:ExifToolVersion",
	"System:FileName", "System:Directory", "System:FileSize", "System:FileModifyDate",
	"System:FileAccessDate", "System:FileInodeChangeDate", "System:FilePermissions",
	"File:FileType", "File:FileTypeExtension", "File:MIMEType",
	"Composite:ImageSize", "Composite:Megapixels",
)

var exifBasics = allow(
	"File:ExifByteOrder",
	"IFD0:Orientation", "IFD0:ResolutionUnit", "IFD0:XResolution", "IFD0:YResolution", "IFD0:YCbCrPositioning",
	"ExifIFD:ExifVersion", "ExifIFD:ComponentsConfiguration", "ExifIFD:ColorSpace", "ExifIFD:Gamma",
	"InteropIFD:InteropIndex", "InteropIFD:InteropVersion",
)

var iccProfile = allow(
	"ICC_Profile:*", "ICC-header:*", "ICC-meas:*", "ICC-view:*", "ICC-chrm:*",
)

var allowlists = map[Container][]rule{
	ContainerJPEG: slices.Concat(common, exifBasics, iccProfile, allow(
		"File:ImageWidth", "File:ImageHeight", "File:EncodingProcess", "File:BitsPerSample",
		"File:ColorComponents", "File:YCbCrSubSampling",
		"JFIF:JFIFVersion", "JFIF:ResolutionUnit", "JFIF:XResolution", "JFIF:YResolution",
		"Adobe:DCTEncodeVersion", "Adobe:APP14Flags0", "Adobe:APP14Flags1", "Adobe:ColorTransform",
	)),
	ContainerPNG: slices.Concat(common, exifBasics, iccProfile, allow(
		"PNG:ImageWidth", "PNG:ImageHeight", "PNG:BitDepth", "PNG:ColorType", "PNG:Compression",
		"PNG:Filter", "PNG:Interlace", "PNG:WhitePointX", "PNG:WhitePointY", "PNG:RedX", "PNG:RedY",
		"PNG:GreenX", "PNG:GreenY", "PNG:BlueX", "PNG:BlueY", "PNG:Gamma", "PNG:SRGBRendering",
		"PNG:BackgroundColor", "PNG:PixelsPerUnitX", "PNG:PixelsPerUnitY", "PNG:PixelUnits",
		"PNG:Palette", "PNG:Transparency", "PNG:SignificantBits", "PNG:ProfileName",
		"PNG:ColorPrimaries", "PNG:TransferCharacteristics", "PNG:MatrixCoefficients", "PNG:VideoFullRangeFlag",
	)),
	ContainerWebP: slices.Concat(common, exifBasics, iccProfile, allow(
		"RIFF:WebP_Flags", "RIFF:ImageWidth", "RIFF:ImageHeight", "RIFF:VP8Version", "RIFF:HorizontalScale",
		"RIFF:VerticalScale", "RIFF:AlphaIsUsed", "RIFF:AlphaPreprocessing", "RIFF:AlphaFiltering",
		"RIFF:AlphaCompression",
	)),
	ContainerHEIC: heifRules,
	ContainerAVIF: heifRules,
	ContainerMP4:  videoRules,
	ContainerMOV:  videoRules,
}

var heifRules = slices.Concat(common, exifBasics, iccProfile, allow(
	"File:ImageWidth", "File:ImageHeight",
	"QuickTime:MajorBrand", "QuickTime:MinorVersion", "QuickTime:CompatibleBrands", "QuickTime:HandlerType",
	"QuickTime:ImageSpatialExtent", "QuickTime:ImagePixelDepth", "QuickTime:Rotation", "QuickTime:ImageRotation",
	"QuickTime:Mirroring",
	"QuickTime:AV1ConfigurationVersion", "QuickTime:SeqProfile", "QuickTime:SeqLevelIdx0", "QuickTime:SeqTier0",
	"QuickTime:HighBitDepth", "QuickTime:TwelveBit", "QuickTime:Monochrome", "QuickTime:ChromaSubsamplingX",
	"QuickTime:ChromaSubsamplingY", "QuickTime:ChromaFormat", "QuickTime:ChromaSamplePosition",
	"QuickTime:InitialDelaySamples",
	"QuickTime:HEVCConfigurationVersion", "QuickTime:GeneralProfileSpace", "QuickTime:GeneralTierFlag",
	"QuickTime:GeneralProfileIDC", "QuickTime:GenProfileCompatibilityFlags", "QuickTime:ConstraintIndicatorFlags",
	"QuickTime:GeneralLevelIDC", "QuickTime:MinSpatialSegmentationIDC", "QuickTime:ParallelismType",
	"QuickTime:BitDepthLuma", "QuickTime:BitDepthChroma", "QuickTime:AverageFrameRate",
	"QuickTime:ConstantFrameRate", "QuickTime:NumTemporalLayers", "QuickTime:TemporalIDNested",
	"QuickTime:ColorProfiles", "QuickTime:ColorPrimaries", "QuickTime:TransferCharacteristics",
	"QuickTime:MatrixCoefficients", "QuickTime:VideoFullRangeFlag", "QuickTime:AuxiliaryImageType",
	"QuickTime:MediaDataSize", "QuickTime:MediaDataOffset", "QuickTime:MediaData",
	"QuickTime:PixelAspectRatio", "QuickTime:CleanAperture*",
	"Meta:PrimaryItemReference", "Meta:MetaImageSize",
	"Composite:Rotation",
	// Padding: checkISO requires top-level free boxes to be all zeros.
	"QuickTime:Free",
), []rule{
	{pattern: "QuickTime:HandlerDescription", value: oneOf("", "PictureHandler", "Picture Handler", "libavif")},
	{pattern: "QuickTime:Unknown_free", value: oneOf("")},
	// exiftool can't remove the XMP of the HDR gain map item in iPhone
	// HEICs; it holds only these two version strings.
	{pattern: "XMP-x:XMPToolkit", value: matchesRe(`^XMP Core [0-9.]+$`)},
	{pattern: "XMP-HDRGainMap:HDRGainMapVersion", value: matchesRe(`^[0-9]+(\.[0-9]+){0,3}$`)},
	{pattern: "ExifTool:Warning", value: oneOf("Item info entries are out of order")},
})

var videoRules = slices.Concat(common, allow(
	"QuickTime:MajorBrand", "QuickTime:MinorVersion", "QuickTime:CompatibleBrands", "QuickTime:MovieHeaderVersion",
	"QuickTime:TimeScale", "QuickTime:Duration", "QuickTime:PreferredRate", "QuickTime:PreferredVolume",
	"QuickTime:MatrixStructure", "QuickTime:PreviewTime", "QuickTime:PreviewDuration", "QuickTime:PosterTime",
	"QuickTime:SelectionTime", "QuickTime:SelectionDuration", "QuickTime:CurrentTime", "QuickTime:NextTrackID",
	"QuickTime:HandlerType", "QuickTime:HandlerVendorID", "QuickTime:Free", "QuickTime:Wide",
	"QuickTime:MediaDataSize", "QuickTime:MediaDataOffset", "QuickTime:MediaData",
	"Track*:TrackHeaderVersion", "Track*:TrackID", "Track*:TrackDuration", "Track*:TrackLayer",
	"Track*:TrackVolume", "Track*:MatrixStructure", "Track*:ImageWidth", "Track*:ImageHeight",
	"Track*:MediaHeaderVersion", "Track*:MediaTimeScale", "Track*:MediaDuration", "Track*:MediaLanguageCode",
	"Track*:HandlerClass", "Track*:HandlerType", "Track*:GraphicsMode", "Track*:OpColor", "Track*:Balance",
	"Track*:CompressorID", "Track*:VendorID", "Track*:SourceImageWidth", "Track*:SourceImageHeight",
	"Track*:XResolution", "Track*:YResolution", "Track*:BitDepth", "Track*:PixelAspectRatio",
	"Track*:VideoFrameRate", "Track*:VideoFieldOrder", "Track*:ColorProfiles", "Track*:ColorPrimaries",
	"Track*:TransferCharacteristics", "Track*:MatrixCoefficients", "Track*:VideoFullRangeFlag",
	"Track*:AVCConfiguration", "Track*:HEVCConfiguration",
	"Track*:AudioFormat", "Track*:AudioBitsPerSample", "Track*:AudioSampleRate", "Track*:AudioChannels",
	"Track*:AudioVendorID", "Track*:PurchaseFileFormat", "Track*:LayoutFlags", "Track*:ChannelLayout*",
	"Track*:AudioChannelTypes", "Track*:AverageBitrate", "Track*:MaxBitrate", "Track*:BufferSize",
	"Track*:TimeToSampleTable", "Track*:SyncSampleTable", "Track*:CompositionTimeToSample",
	"Track*:SampleToChunk", "Track*:SampleSizes", "Track*:ChunkOffset", "Track*:ChunkOffset64",
	"Track*:SampleGroupDescription", "Track*:SampleToGroup", "Track*:IdependentAndDisposableSamples",
	"Track*:CleanAperture*", "Track*:ProductionAperture*", "Track*:EncodedPixels*",
	"Track*:Unknown_edts", "Track*:Unknown_esds", "Track*:Unknown_hvcC", "Track*:Unknown_avcC",
	"Track*:Unknown_dvcC", "Track*:Unknown_dvvC", "Track*:Unknown_btrt", "Track*:Unknown_colr",
	"Track*:Unknown_pasp", "Track*:Unknown_clli", "Track*:Unknown_mdcv", "Track*:Unknown_chan",
	"Track*:Unknown_mp4a", "Track*:Unknown_dOps", "Track*:Unknown_dac3", "Track*:Unknown_dec3",
	"Composite:AvgBitrate", "Composite:Rotation",
), []rule{
	{pattern: "QuickTime:CreateDate", value: oneOf(zeroDate)},
	{pattern: "QuickTime:ModifyDate", value: oneOf(zeroDate)},
	{pattern: "Track*:TrackCreateDate", value: oneOf(zeroDate)},
	{pattern: "Track*:TrackModifyDate", value: oneOf(zeroDate)},
	{pattern: "Track*:MediaCreateDate", value: oneOf(zeroDate)},
	{pattern: "Track*:MediaModifyDate", value: oneOf(zeroDate)},
	{pattern: "Track*:HandlerDescription", value: oneOf("", "VideoHandler", "SoundHandler", "DataHandler")},
})

// forbidden patterns are checked on their own, case-insensitively, so an
// over-broad allowlist entry still can't let them through. Values are
// group:tag patterns.
var forbidden = []string{
	"*:gps*", "*:*location*", "*:*latitude*", "*:*longitude*", "*:*serial*",
	"*:*make*", "*:*model*", "*:software", "*:*owner*", "*:*artist*", "*:*author*", "*:*creator*",
	"*:*copyright*", "*:*comment*", "*:*description*", "*:*title*", "*:*keyword*", "*:*caption*",
	"*:*city*", "*:*country*", "*:*user*", "*:*person*", "*:*face*", "*:*region*",
	"*:*date*", "*:*time*stamp*", "*:*timezone*", "*:*offsettime*",
	"*:*embedded*", "*:*motionphoto*", "*:*microvideo*", "*:*thumbnailimage*", "*:*previewimage*",
	"*:*jpgfromraw*", "*:*otherimage*", "*:warning", "*:error",
	"keys:*", "userdata:*", "itemlist:*", "xmp*:*", "makernotes:*", "apple:*", "trailer:*", "samsung:*",
	"google:*", "mpf*:*", "iptc*:*", "photoshop:*", "gps:*", "exififd:*subsec*", "exififd:*original*",
	"*:*cameraownername*", "*:*lens*", "*:*uniqueid*", "*:*documentname*", "*:*hostcomputer*",
}

// forbiddenExempt are tags that match a forbidden pattern but are known
// safe: filesystem facts about the cache's own temp file, ICC profile
// header fields (the profile's own creation date, not the photo's), the
// zeroed container dates checked by value in the allowlist, and ffmpeg's
// stream-copy timing.
var forbiddenExempt = []string{
	"System:*", "ICC-header:ProfileDateTime", "ICC-chrm:*", "ICC_Profile:ProfileDescription*", "ICC_Profile:ProfileCopyright",
	"ICC-header:ProfileCreator", "ICC-header:DeviceModel", "ICC-header:DeviceManufacturer",
	"ICC_Profile:DeviceModelDesc", "ICC_Profile:DeviceMfgDesc", "ICC-header:PrimaryPlatform",
	"QuickTime:CreateDate", "QuickTime:ModifyDate",
	"Track*:TrackCreateDate", "Track*:TrackModifyDate", "Track*:MediaCreateDate", "Track*:MediaModifyDate",
	"QuickTime:PreviewTime", "QuickTime:PosterTime", "QuickTime:SelectionTime", "QuickTime:CurrentTime",
	"Track*:CompositionTimeToSample", "Track*:TimeToSampleTable", "Track*:SampleGroupDescription",
	"QuickTime:HandlerDescription", "Track*:HandlerDescription", "QuickTime:TimeScale", "Track*:MediaTimeScale",
	"QuickTime:PreviewDuration", "QuickTime:SelectionDuration",
	// Value-checked in the HEIF allowlist.
	"XMP-x:XMPToolkit", "XMP-HDRGainMap:HDRGainMapVersion", "ExifTool:Warning",
}

func matches(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// exifDate is a bare date and time, the only form of the dates Process
// writes.
var exifDate = matchesRe(`^[0-9]{4}:[0-9]{2}:[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}$`)

var imageDates = []rule{
	{pattern: "ExifIFD:DateTimeOriginal", value: exifDate},
	{pattern: "ExifIFD:OffsetTimeOriginal", value: matchesRe(`^[+-][0-9]{2}:[0-9]{2}$`)},
	{pattern: "Composite:SubSecDateTimeOriginal", value: matchesRe(`^[0-9]{4}:[0-9]{2}:[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}([+-][0-9]{2}:[0-9]{2})?$`)},
}

var videoDates = []rule{
	{pattern: "QuickTime:CreateDate", value: exifDate},
	{pattern: "QuickTime:ModifyDate", value: exifDate},
	{pattern: "Track*:TrackCreateDate", value: exifDate},
	{pattern: "Track*:TrackModifyDate", value: exifDate},
	{pattern: "Track*:MediaCreateDate", value: exifDate},
	{pattern: "Track*:MediaModifyDate", value: exifDate},
}

// dateRules allow the date taken that Process writes when dates are kept.
// They take precedence over the forbidden patterns.
var dateRules = map[Container][]rule{
	ContainerJPEG: imageDates, ContainerWebP: imageDates,
	ContainerHEIC: imageDates, ContainerAVIF: imageDates,
	ContainerMP4: videoDates, ContainerMOV: videoDates,
}

// checkTags returns the group:tag names that fail verification for kind.
// dates allows the dates in dateRules.
func checkTags(k Container, tags map[string]any, dates bool) []string {
	rules, ok := allowlists[k]
	if !ok {
		return []string{fmt.Sprintf("File:Kind(%s)", k)}
	}
	var bad []string
	for name, val := range tags {
		if name == "SourceFile" {
			continue
		}
		if dates && allowed(dateRules[k], name, val) {
			continue
		}
		if isForbidden(name) || !allowed(rules, name, val) {
			bad = append(bad, name)
		}
	}
	slices.Sort(bad)
	return bad
}

func allowed(rules []rule, name string, val any) bool {
	for _, r := range rules {
		if matches(r.pattern, name) {
			return r.value == nil || r.value(val)
		}
	}
	return false
}

func isForbidden(name string) bool {
	for _, p := range forbiddenExempt {
		if matches(p, name) {
			return false
		}
	}
	lower := strings.ToLower(name)
	for _, p := range forbidden {
		if matches(p, lower) {
			return true
		}
	}
	return false
}
