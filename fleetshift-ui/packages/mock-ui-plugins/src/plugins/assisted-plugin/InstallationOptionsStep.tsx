import {
  Checkbox,
  Content,
  Form,
  FormGroup,
  Stack,
  StackItem,
  Title,
} from "@patternfly/react-core";

import type { AssistedFormData } from "./CreateAssistedWizard";

interface InstallationOptionsStepProps {
  formData: AssistedFormData;
  onChange: <K extends keyof AssistedFormData>(
    field: K,
    value: AssistedFormData[K],
  ) => void;
}

export default function InstallationOptionsStep({
  formData,
  onChange,
}: InstallationOptionsStepProps) {
  const updateCompliance = (
    key: keyof AssistedFormData["compliance"],
    value: boolean,
  ) => {
    onChange("compliance", { ...formData.compliance, [key]: value });
  };

  return (
    <Stack hasGutter>
      <StackItem>
        <Title headingLevel="h3">Installation options</Title>
        <Content component="p">
          Optional installation settings for this cluster.
        </Content>
      </StackItem>
      <StackItem>
        <Form>
          <FormGroup label="Options" fieldId="assisted-install-options">
            <Checkbox
              id="assisted-compliance-fips"
              label="Enable FIPS mode"
              isChecked={formData.compliance.fips}
              onChange={(_e, checked) => updateCompliance("fips", checked)}
            />
            <Checkbox
              id="assisted-compliance-disconnected"
              label="Fully disconnected (air-gapped) installation"
              isChecked={formData.compliance.disconnected}
              onChange={(_e, checked) =>
                updateCompliance("disconnected", checked)
              }
            />
          </FormGroup>
        </Form>
      </StackItem>
    </Stack>
  );
}
